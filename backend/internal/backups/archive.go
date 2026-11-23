package backups

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
)

const fileFormat = "sub-pool-pg-v1"
const fileHeader = "SUB-POOL-PG-BACKUP/1\n"
const maxPlaintext = 8 << 20
const maxFile = 12 << 20
const maxJSON = 16 << 20
const maxTableRows = 100000
const maxTotalRows = 250000

var (
	ErrInvalid = &auth.Failure{Status: 400, Code: "BACKUP_INVALID"}
	ErrSchema  = &auth.Failure{Status: 409, Code: "BACKUP_SCHEMA_INCOMPATIBLE"}
	ErrLimit   = &auth.Failure{Status: 413, Code: "BACKUP_LIMIT_EXCEEDED"}
	ErrKey     = &auth.Failure{Status: 503, Code: "SECRET_STORAGE_UNAVAILABLE"}
	ErrBusy    = &auth.Failure{Status: 409, Code: "RESTORE_BUSY"}
)

type tableData struct {
	Name    string            `json:"name"`
	Columns []string          `json:"columns"`
	Rows    []json.RawMessage `json:"rows"`
}
type sequenceNext struct {
	Table  string `json:"table"`
	Column string `json:"column"`
	Next   string `json:"next"`
}
type snapshot struct {
	Format            string            `json:"format"`
	CreatedAt         time.Time         `json:"created_at"`
	IncludeSecrets    bool              `json:"include_secrets"`
	SchemaFingerprint string            `json:"schema_fingerprint"`
	Migrations        map[string]string `json:"migrations"`
	Excluded          []string          `json:"excluded"`
	Tables            []tableData       `json:"tables"`
	Sequences         []sequenceNext    `json:"sequences"`
}
type TableCount struct {
	Name string `json:"name"`
	Rows string `json:"rows"`
}
type Manifest struct {
	Format            string       `json:"format"`
	Scope             string       `json:"scope,omitempty"`
	SourceCutoff      time.Time    `json:"source_cutoff"`
	IncludeSecrets    bool         `json:"include_secrets"`
	SchemaFingerprint string       `json:"schema_fingerprint"`
	Rows              string       `json:"rows"`
	Tables            []TableCount `json:"tables"`
	Excluded          []string     `json:"excluded"`
}

func hash(body []byte) string { return fmt.Sprintf("%x", sha256.Sum256(body)) }
func number(n int) string     { return strconv.Itoa(n) }
func newID() (string, error) {
	var value [16]byte
	_, err := rand.Read(value[:])
	return base64.RawURLEncoding.EncodeToString(value[:]), err
}
func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) == 0 {
		return nil, nil
	}
	if len(key) != 32 {
		return nil, ErrKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func (s *Service) protect(data snapshot) (string, error) {
	if s.aead == nil {
		return "", ErrKey
	}
	body, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	if len(body) > maxPlaintext {
		return "", ErrLimit
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	encrypted := s.aead.Seal(nonce, nonce, body, []byte(fileHeader))
	file := fileHeader + base64.StdEncoding.EncodeToString(encrypted) + "\n"
	if len(file) > maxFile {
		return "", ErrLimit
	}
	return file, nil
}
func (s *Service) unprotect(file string) (snapshot, error) {
	var data snapshot
	if s.aead == nil {
		return data, ErrKey
	}
	if len(file) > maxFile {
		return data, ErrLimit
	}
	if !strings.HasPrefix(file, fileHeader) || !strings.HasSuffix(file, "\n") {
		return data, ErrInvalid
	}
	armored := file[len(fileHeader) : len(file)-1]
	encrypted, err := base64.StdEncoding.Strict().DecodeString(armored)
	if err != nil || base64.StdEncoding.EncodeToString(encrypted) != armored || len(encrypted) < s.aead.NonceSize()+s.aead.Overhead() {
		return data, ErrInvalid
	}
	if len(encrypted)-s.aead.NonceSize()-s.aead.Overhead() > maxPlaintext {
		return data, ErrLimit
	}
	nonce := encrypted[:s.aead.NonceSize()]
	body, err := s.aead.Open(nil, nonce, encrypted[len(nonce):], []byte(fileHeader))
	if err != nil || !utf8.Valid(body) {
		return data, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(&data) != nil || d.Decode(new(any)) != io.EOF {
		return data, ErrInvalid
	}
	canonical, err := json.Marshal(data)
	if err != nil || !bytes.Equal(body, canonical) || data.CreatedAt.IsZero() || data.CreatedAt.Location() != time.UTC || data.CreatedAt.Year() < 1970 || data.CreatedAt.Year() > 9998 {
		return data, ErrInvalid
	}
	total := 0
	for _, t := range data.Tables {
		total += len(t.Rows)
		if len(t.Rows) > maxTableRows || total > maxTotalRows {
			return data, ErrLimit
		}
	}
	return data, nil
}

func rowFields(body json.RawMessage, columns []string) bool {
	d := json.NewDecoder(bytes.NewReader(body))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] || !contains(columns, key) {
			return false
		}
		seen[key] = true
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return false
		}
	}
	token, err = d.Token()
	return err == nil && token == json.Delim('}') && len(seen) == len(columns) && d.Decode(new(any)) == io.EOF
}

func capture(ctx context.Context, tx pgx.Tx, shape schema, includeSecrets bool) (snapshot, error) {
	data := snapshot{Format: fileFormat, IncludeSecrets: includeSecrets, SchemaFingerprint: shape.Fingerprint, Migrations: shape.Migrations, Excluded: append([]string{}, excludedTables...), Tables: []tableData{}, Sequences: []sequenceNext{}}
	if _, err := tx.Exec(ctx, `SET LOCAL TIME ZONE 'UTC'`); err != nil {
		return data, err
	}
	if err := tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&data.CreatedAt); err != nil {
		return data, err
	}
	data.CreatedAt = data.CreatedAt.UTC()
	bytesRead, totalRows := 0, 0
	for _, name := range businessTables {
		t := shape.Tables[name]
		item := tableData{Name: name, Columns: names(t.Columns), Rows: []json.RawMessage{}}
		rows, err := tx.Query(ctx, `SELECT row_to_json(r)::text FROM `+shape.table(name)+` r ORDER BY `+columnSQL(t.Primary))
		if err != nil {
			return data, err
		}
		for rows.Next() {
			var raw string
			if err = rows.Scan(&raw); err != nil {
				break
			}
			row := json.RawMessage(raw)
			if !includeSecrets && (name == "connection_config" || name == "notification_config") {
				var fields map[string]json.RawMessage
				if err = json.Unmarshal(row, &fields); err != nil {
					break
				}
				fields["secret"] = json.RawMessage("null")
				if name == "connection_config" {
					fields["credential_kind"] = json.RawMessage("null")
				}
				row, err = json.Marshal(fields)
				if err != nil {
					break
				}
			}
			bytesRead += len(row)
			totalRows++
			if bytesRead > maxPlaintext || totalRows > maxTotalRows || len(item.Rows) >= maxTableRows {
				err = ErrLimit
				break
			}
			item.Rows = append(item.Rows, row)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return data, err
		}
		data.Tables = append(data.Tables, item)
		if contains(singletonTables, name) && len(item.Rows) != 1 {
			return data, ErrInvalid
		}
		for _, c := range t.Columns {
			if c.Sequence == nil {
				continue
			}
			var last int64
			var called bool
			if err = tx.QueryRow(ctx, `SELECT last_value,is_called FROM `+ident(shape.Namespace, c.Sequence.Name)).Scan(&last, &called); err != nil {
				return data, err
			}
			if called {
				if last == math.MaxInt64 {
					return data, ErrLimit
				}
				last++
			}
			data.Sequences = append(data.Sequences, sequenceNext{Table: name, Column: c.Name, Next: strconv.FormatInt(last, 10)})
		}
	}
	body, err := json.Marshal(data)
	if err != nil {
		return data, err
	}
	if len(body) > maxPlaintext {
		return data, ErrLimit
	}
	return data, nil
}

func manifest(data snapshot) Manifest {
	result := Manifest{Format: data.Format, SourceCutoff: data.CreatedAt, IncludeSecrets: data.IncludeSecrets, SchemaFingerprint: data.SchemaFingerprint, Tables: []TableCount{}, Excluded: append([]string{}, data.Excluded...)}
	total := 0
	for _, t := range data.Tables {
		total += len(t.Rows)
		result.Tables = append(result.Tables, TableCount{Name: t.Name, Rows: number(len(t.Rows))})
	}
	result.Rows = number(total)
	return result
}
func basis(data snapshot) string {
	data.CreatedAt = time.Time{}
	body, _ := json.Marshal(data)
	return hash(body)
}

func validateSequences(ctx context.Context, tx pgx.Tx, shape schema, data snapshot) error {
	i := 0
	for _, name := range businessTables {
		for _, c := range shape.Tables[name].Columns {
			if c.Sequence == nil {
				continue
			}
			if i >= len(data.Sequences) {
				return ErrInvalid
			}
			seq := data.Sequences[i]
			i++
			next, err := strconv.ParseInt(seq.Next, 10, 64)
			if err != nil || next < 1 || strconv.FormatInt(next, 10) != seq.Next || seq.Table != name || seq.Column != c.Name {
				return ErrInvalid
			}
			var maximum int64
			if err = tx.QueryRow(ctx, `SELECT COALESCE(max(`+ident(c.Name)+`),0)::bigint FROM `+tempTable(name)).Scan(&maximum); err != nil {
				return ErrInvalid
			}
			limit, err := strconv.ParseInt(c.Sequence.Max, 10, 64)
			if err != nil || next <= maximum || next > limit {
				return ErrInvalid
			}
		}
	}
	if i != len(data.Sequences) {
		return ErrInvalid
	}
	return nil
}
