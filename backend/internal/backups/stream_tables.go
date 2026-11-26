package backups

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var lightTables = []string{
	"auth_users", "auth_api_keys", "connection_config", "connection_sources", "estimate_config", "managed_accounts",
	"monitoring_config", "notification_config", "observation_management", "participant_bindings", "participants",
	"pool_contract_accounts", "pool_contract_participants", "pool_contracts", "pools", "security_config", "source_blocks",
	"system_settings", "upstream_write_grants", "wallet_automation",
}

func included(scope, table string) bool {
	return scope == fullScope || slices.Contains(lightTables, table)
}

func writeSize(out io.Writer, size uint32) error {
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], size)
	_, err := out.Write(prefix[:])
	return err
}

func readSize(in io.Reader) (uint32, error) {
	var prefix [4]byte
	err := readArchivePart(in, prefix[:])
	return binary.BigEndian.Uint32(prefix[:]), err
}

type streamSchema struct {
	SchemaFingerprint string            `json:"schema_fingerprint"`
	Migrations        map[string]string `json:"migrations"`
}

func captureStream(ctx context.Context, tx pgx.Tx, shape schema, scope string, secrets bool, destination io.Writer) (streamFooter, error) {
	footer := streamFooter{
		Manifest: Manifest{
			Format: streamFormat, Scope: scope, IncludeSecrets: secrets, SchemaFingerprint: shape.Fingerprint,
			Tables: []TableCount{}, Excluded: excludedTables,
		},
		Migrations: shape.Migrations, Sequences: []sequenceNext{}, Pending: []string{},
	}
	if _, err := tx.Exec(ctx, `SET LOCAL TIME ZONE 'UTC'`); err != nil {
		return footer, err
	}
	if err := tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&footer.Manifest.SourceCutoff); err != nil {
		return footer, err
	}
	footer.Manifest.SourceCutoff = footer.Manifest.SourceCutoff.UTC()
	if scope == lightScope {
		var err error
		footer.Pending, err = restoreBlockers(ctx, tx, shape, false)
		if err != nil {
			return footer, err
		}
	}
	digest := sha256.New()
	out := io.MultiWriter(destination, digest)
	header, err := json.Marshal(streamSchema{shape.Fingerprint, shape.Migrations})
	if err != nil {
		return footer, err
	}
	if err = writeSize(out, uint32(len(header))); err != nil {
		return footer, err
	}
	if _, err = out.Write(header); err != nil {
		return footer, err
	}
	var total int64
	for _, name := range archiveBusinessTables(shape) {
		if !included(scope, name) {
			continue
		}
		t := shape.Tables[name]
		count, err := captureTable(ctx, tx, shape, t, secrets, out)
		if err != nil {
			return footer, err
		}
		if contains(singletonTables, name) && count != 1 {
			return footer, ErrInvalid
		}
		total += count
		footer.Manifest.Tables = append(footer.Manifest.Tables, TableCount{Name: name, Rows: strconv.FormatInt(count, 10)})
		for _, c := range t.Columns {
			if c.Sequence == nil {
				continue
			}
			var last int64
			var called bool
			if err = tx.QueryRow(ctx, `SELECT last_value,is_called FROM `+ident(shape.Namespace, c.Sequence.Name)).Scan(&last, &called); err != nil {
				return footer, err
			}
			if called {
				if last == math.MaxInt64 {
					return footer, ErrLimit
				}
				last++
			}
			footer.Sequences = append(footer.Sequences, sequenceNext{Table: name, Column: c.Name, Next: strconv.FormatInt(last, 10)})
		}
	}
	footer.Manifest.Rows = strconv.FormatInt(total, 10)
	footer.BodySHA256 = fmt.Sprintf("%x", digest.Sum(nil))
	return footer, nil
}

func captureTable(ctx context.Context, tx pgx.Tx, shape schema, table tableSchema, secrets bool, out io.Writer) (int64, error) {
	const cursor = "sp_backup_rows"
	projection := `row_to_json(r)::text`
	if !secrets {
		switch table.Name {
		case "connection_config":
			projection = `(row_to_json(r)::jsonb || '{"secret":null,"credential_kind":null}'::jsonb)::text`
		case "notification_config":
			projection = `(row_to_json(r)::jsonb || '{"secret":null}'::jsonb)::text`
		}
	}
	// End each FETCH before writing encrypted chunks on the snapshot transaction.
	_, err := tx.Exec(ctx, `DECLARE `+cursor+` NO SCROLL CURSOR FOR
		SELECT CASE WHEN part.start=1 THEN octet_length(encoded.body) ELSE 0 END,
		       substring(encoded.body FROM part.start FOR 131072)
		FROM `+shape.table(table.Name)+` r
		CROSS JOIN LATERAL (SELECT convert_to(`+projection+`,'UTF8') AS body OFFSET 0) encoded
		CROSS JOIN LATERAL generate_series(1,octet_length(encoded.body),131072) AS part(start)
		ORDER BY `+columnSQL(table.Primary)+`,part.start`)
	if err != nil {
		return 0, err
	}
	var count int64
	var batch [32]struct {
		size uint32
		data []byte
	}
	for {
		rows, err := tx.Query(ctx, `FETCH FORWARD 32 FROM `+cursor)
		if err != nil {
			return 0, err
		}
		n := 0
		for rows.Next() {
			part := &batch[n]
			if err = rows.Scan(&part.size, &part.data); err != nil {
				break
			}
			n++
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return 0, err
		}
		for i := 0; i < n; i++ {
			part := &batch[i]
			if part.size != 0 {
				count++
				if err = writeSize(out, part.size); err != nil {
					return 0, err
				}
			}
			if _, err = out.Write(part.data); err != nil {
				return 0, err
			}
			part.data = nil
		}
		if n < len(batch) {
			break
		}
	}
	if _, err = tx.Exec(ctx, `CLOSE `+cursor); err != nil {
		return 0, err
	}
	return count, writeSize(out, 0)
}

func streamBasis(footer streamFooter) string {
	footer.Manifest.SourceCutoff = time.Time{}
	body, _ := json.Marshal(footer)
	return hash(body)
}

func (s *Service) loadStreamCandidate(ctx context.Context, tx pgx.Tx, shape schema, input io.Reader, maxBytes int64) (streamFooter, error) {
	var footer streamFooter
	decoder, err := newStreamDecoder(input, s.key)
	if err != nil {
		return footer, err
	}
	digest := sha256.New()
	body := io.TeeReader(decoder, digest)
	size, err := readSize(body)
	if err != nil {
		return footer, err
	}
	if size == 0 || size > frameSize {
		return footer, ErrInvalid
	}
	header := make([]byte, size)
	if err = readArchivePart(body, header); err != nil {
		return footer, err
	}
	var meta streamSchema
	if json.Unmarshal(header, &meta) != nil {
		return footer, ErrInvalid
	}
	sourceShape := shape
	if meta.SchemaFingerprint != shape.Fingerprint || !reflect.DeepEqual(meta.Migrations, shape.Migrations) {
		sourceShape, err = schemaBeforeWeeklyIssuance(shape)
		if err != nil {
			return footer, err
		}
		if meta.SchemaFingerprint != sourceShape.Fingerprint || !reflect.DeepEqual(meta.Migrations, sourceShape.Migrations) {
			sourceShape, err = schemaBeforeSourceExpiry(sourceShape)
			if err != nil {
				return footer, err
			}
			if meta.SchemaFingerprint != sourceShape.Fingerprint || !reflect.DeepEqual(meta.Migrations, sourceShape.Migrations) {
				return footer, ErrSchema
			}
		}
	}
	canonical, _ := json.Marshal(meta)
	if !bytes.Equal(canonical, header) {
		return footer, ErrInvalid
	}

	counts := []TableCount{}
	var total int64
	for _, name := range businessTables {
		if err = createCandidate(ctx, tx, shape, name); err != nil {
			return footer, err
		}
		sourceTable, exists := sourceShape.Tables[name]
		if !exists || !included(decoder.scope, name) {
			continue
		}
		count, err := readCandidateTable(ctx, tx, shape, name, names(sourceTable.Columns), body, maxBytes)
		if err != nil {
			return footer, err
		}
		if contains(singletonTables, name) && count != 1 {
			return footer, ErrInvalid
		}
		total += count
		counts = append(counts, TableCount{Name: name, Rows: strconv.FormatInt(count, 10)})
	}

	footer, err = decoder.finish()
	if err != nil {
		return footer, err
	}
	m := footer.Manifest
	if m.Format != streamFormat || m.Scope != decoder.scope || m.SchemaFingerprint != meta.SchemaFingerprint ||
		!reflect.DeepEqual(footer.Migrations, meta.Migrations) || !reflect.DeepEqual(m.Excluded, excludedTables) ||
		m.Rows != strconv.FormatInt(total, 10) || !reflect.DeepEqual(m.Tables, counts) ||
		m.SourceCutoff.Location() != time.UTC || m.SourceCutoff.Year() < 1970 || m.SourceCutoff.Year() > 9998 ||
		footer.BodySHA256 != fmt.Sprintf("%x", digest.Sum(nil)) || footer.Pending == nil ||
		m.Scope == fullScope && len(footer.Pending) != 0 || footer.Sequences == nil {
		return footer, ErrInvalid
	}
	for i, reason := range footer.Pending {
		switch reason {
		case "COLLECTION_ACTIVE", "MAIL_ACTIVE", "MAINTENANCE_ACTIVE", "REMOTE_WRITE_UNCONFIRMED":
		default:
			return footer, ErrInvalid
		}
		if slices.Contains(footer.Pending[:i], reason) {
			return footer, ErrInvalid
		}
	}
	sequences, err := completeSequences(sourceShape, m.Scope, footer.Sequences)
	if err != nil {
		return footer, err
	}
	if _, exists := sourceShape.Tables["weekly_wallet_issuances"]; !exists {
		sequence := shape.Tables["weekly_wallet_issuances"].Columns[0].Sequence
		sequences = append(sequences, sequenceNext{Table: "weekly_wallet_issuances", Column: "id", Next: sequence.Start})
	}
	if err = validateCandidate(ctx, tx, shape, m.IncludeSecrets, sequences); err != nil {
		return footer, err
	}
	footer.Sequences = sequences
	return footer, nil
}

func readCandidateTable(ctx context.Context, tx pgx.Tx, shape schema, name string, columns []string, body io.Reader, maxBytes int64) (int64, error) {
	var batch bytes.Buffer
	batch.WriteByte('[')
	flush := func() error {
		if batch.Len() == 1 {
			return nil
		}
		batch.WriteByte(']')
		if err := insertCandidate(ctx, tx, shape, name, columns, batch.Bytes()); err != nil {
			return err
		}
		if batch.Cap() > 2*frameSize {
			batch = bytes.Buffer{}
		} else {
			batch.Reset()
		}
		batch.WriteByte('[')
		return nil
	}
	var count int64
	for {
		size, err := readSize(body)
		if err != nil {
			return 0, err
		}
		if size == 0 {
			return count, flush()
		}
		if int64(size) > maxBytes {
			return 0, ErrInvalid
		}
		if int64(batch.Len())+int64(size) > frameSize {
			if err = flush(); err != nil {
				return 0, err
			}
		}
		if batch.Len() > 1 {
			batch.WriteByte(',')
		}
		start := batch.Len()
		if _, err = io.CopyN(&batch, body, int64(size)); err != nil {
			if errors.Is(err, io.EOF) {
				err = ErrInvalid
			}
			return 0, err
		}
		row := batch.Bytes()[start:]
		if !utf8.Valid(row) || !rowFields(row, columns) {
			return 0, ErrInvalid
		}
		count++
	}
}

func completeSequences(shape schema, scope string, includedSequences []sequenceNext) ([]sequenceNext, error) {
	all := make([]sequenceNext, 0, len(includedSequences))
	i := 0
	for _, name := range archiveBusinessTables(shape) {
		for _, c := range shape.Tables[name].Columns {
			if c.Sequence == nil {
				continue
			}
			if !included(scope, name) {
				all = append(all, sequenceNext{Table: name, Column: c.Name, Next: c.Sequence.Start})
				continue
			}
			if i == len(includedSequences) || includedSequences[i].Table != name || includedSequences[i].Column != c.Name {
				return nil, ErrInvalid
			}
			all = append(all, includedSequences[i])
			i++
		}
	}
	if i != len(includedSequences) {
		return nil, ErrInvalid
	}
	return all, nil
}
