package backups

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/telemetry"
)

func (s *Service) saveStreamArchive(ctx context.Context, tx pgx.Tx, shape schema, actor auth.Identity, id, kind, scope string, secrets bool) (Archive, string, error) {
	blob, err := newBlobWriter(ctx, tx, s.MaxBytes)
	if err != nil {
		return Archive{}, "", err
	}
	encoder, err := newStreamEncoder(blob, s.key, scope)
	if err != nil {
		return Archive{}, "", err
	}
	footer, err := captureStream(ctx, tx, shape, scope, secrets, encoder)
	if err == nil {
		err = encoder.finish(footer)
	}
	if err == nil {
		err = blob.flush()
	}
	if err != nil {
		return Archive{}, "", err
	}
	body, err := json.Marshal(footer.Manifest)
	if err != nil {
		return Archive{}, "", err
	}
	basis := streamBasis(footer)
	var before *string
	if kind == "pre_restore" {
		before = &basis
	}
	out, err := scanArchive(tx.QueryRow(ctx, `INSERT INTO backup_archives
		(id,kind,created_at,actor_id,actor_username,include_secrets,source_cutoff,file_sha256,blob_id,file_bytes,manifest,snapshot_basis)
		VALUES($1,$2,clock_timestamp(),$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING `+archiveColumns,
		id, kind, actor.ID, actor.Username, secrets, footer.Manifest.SourceCutoff, fmt.Sprintf("%x", blob.sum.Sum(nil)), blob.id, blob.size, body, before))
	return out, basis, err
}

func (s *Service) loadArchiveCandidate(ctx context.Context, tx pgx.Tx, shape schema, input io.Reader, maxBytes int64) (streamFooter, error) {
	buffer := bufio.NewReader(input)
	header, err := buffer.Peek(len(streamHeader))
	if err != nil {
		if errors.Is(err, io.EOF) {
			err = ErrInvalid
		}
		return streamFooter{}, err
	}
	switch string(header) {
	case streamHeader:
		return s.loadStreamCandidate(ctx, tx, shape, buffer, maxBytes)
	case fileHeader:
		file, err := io.ReadAll(io.LimitReader(buffer, maxFile+1))
		if err != nil {
			return streamFooter{}, err
		}
		data, err := s.unprotect(string(file))
		if err != nil {
			return streamFooter{}, err
		}
		candidate, err := nativeFinancialCompatibleSnapshot(shape, data)
		if err != nil {
			return streamFooter{}, err
		}
		if err = validateRows(ctx, tx, shape, candidate); err != nil {
			return streamFooter{}, err
		}
		return streamFooter{Manifest: manifest(data), Sequences: candidate.Sequences}, nil
	default:
		if strings.HasPrefix(string(header), "SUB-POOL-PG-BACKUP/") {
			return streamFooter{}, ErrSchema
		}
		return streamFooter{}, ErrInvalid
	}
}

func downloadedProof(token, planID, digest string) string {
	return hash([]byte(token + "\n" + planID + "\n" + digest))
}

func planForSession(out Plan, token string) Plan {
	out = projectExpiry(out)
	out.BeforeDownloaded = out.State == "ready" && out.beforeProof != nil && out.beforeHash != nil &&
		*out.beforeProof == downloadedProof(token, out.ID, *out.beforeHash)
	return out
}

func (s *Service) preflightFile(ctx context.Context, token, id string, file io.Reader) (Plan, error) {
	return s.preflightStream(ctx, token, id, file, "")
}

func (s *Service) preflightStream(ctx context.Context, token, id string, file io.Reader, sourceID string) (Plan, error) {
	if !objectID.MatchString(id) {
		return Plan{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token)
	if err != nil {
		return Plan{}, err
	}
	defer tx.Rollback(context.Background())
	if s.aead == nil {
		return Plan{}, ErrKey
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,772))`, id); err != nil {
		return Plan{}, err
	}
	var upload storedUpload
	if sourceID != "" {
		var legacy, blob *string
		err = tx.QueryRow(ctx, `SELECT file,blob_id,COALESCE(file_bytes,octet_length(file)),file_sha256 FROM backup_archives WHERE id=$1`, sourceID).
			Scan(&legacy, &blob, &upload.size, &upload.sum)
		if errors.Is(err, pgx.ErrNoRows) {
			return Plan{}, auth.ErrNotFound
		}
		if err != nil {
			return Plan{}, err
		}
		if upload.size > s.MaxBytes {
			return Plan{}, ErrLimit
		}
		if blob != nil {
			upload.id = *blob
			file = upload.reader(ctx, tx)
		} else {
			if legacy == nil || hash([]byte(*legacy)) != upload.sum || int64(len(*legacy)) != upload.size {
				return Plan{}, ErrInvalid
			}
			file = strings.NewReader(*legacy)
		}
	}
	var owner, previousHash string
	err = tx.QueryRow(ctx, `SELECT actor_id,file_sha256 FROM restore_plans WHERE id=$1`, id).Scan(&owner, &previousHash)
	if err == nil {
		if owner != actor.ID {
			return Plan{}, auth.ErrConflict
		}
		if sourceID == "" {
			digest := sha256.New()
			if _, err = io.Copy(digest, file); err != nil {
				return Plan{}, err
			}
			upload.sum = fmt.Sprintf("%x", digest.Sum(nil))
		}
		if previousHash != upload.sum {
			return Plan{}, auth.ErrConflict
		}
		out, err := scanPlan(tx.QueryRow(ctx, `SELECT `+planColumns+` FROM restore_plans WHERE id=$1`, id))
		if err == nil {
			err = s.finish(ctx, tx, token)
		}
		return planForSession(out, token), err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Plan{}, err
	}
	shape, err := readSchema(ctx, tx)
	if err != nil {
		return Plan{}, err
	}
	var writer *blobWriter
	if upload.id == "" {
		writer, err = newBlobWriter(ctx, tx, s.MaxBytes)
		if err != nil {
			return Plan{}, err
		}
		file = io.TeeReader(file, writer)
	}
	footer, err := s.loadArchiveCandidate(ctx, tx, shape, file, s.MaxBytes)
	if writer != nil && writer.err != nil {
		return Plan{}, writer.err
	}
	if err != nil {
		return Plan{}, err
	}
	if writer != nil {
		if err = writer.flush(); err != nil {
			return Plan{}, err
		}
		upload = storedUpload{writer.id, writer.size, fmt.Sprintf("%x", writer.sum.Sum(nil))}
	}

	blockers, err := restoreBlockers(ctx, tx, shape, true)
	if err != nil {
		return Plan{}, err
	}
	for _, reason := range footer.Pending {
		if reason = "BACKUP_" + reason; !slices.Contains(blockers, reason) {
			blockers = append(blockers, reason)
		}
	}
	state, reason, outcome := "ready", "READY", "succeeded"
	if len(blockers) != 0 {
		state, reason, outcome = "blocked", "RESTORE_BLOCKED", "denied"
	}
	if err = journal(ctx, tx, &actor.ID, nil, "backup.preflight", outcome, id); err != nil {
		return Plan{}, err
	}
	var beforeID *string
	var targetBasis string
	var targetTables []TableCount
	if state == "ready" {
		backupID, err := newID()
		if err != nil {
			return Plan{}, err
		}
		before, basis, err := s.saveStreamArchive(ctx, tx, shape, actor, backupID, "pre_restore", fullScope, true)
		if err != nil {
			return Plan{}, err
		}
		beforeID, targetBasis, targetTables = &backupID, basis, before.Tables
	} else {
		target, err := captureStream(ctx, tx, shape, fullScope, true, io.Discard)
		if err != nil {
			return Plan{}, err
		}
		targetBasis, targetTables = streamBasis(target), target.Manifest.Tables
	}
	cleared := []TableCount{}
	if footer.Manifest.Scope == lightScope {
		for _, table := range targetTables {
			if !included(lightScope, table.Name) {
				cleared = append(cleared, table)
			}
		}
	}
	metadata, err := json.Marshal(footer.Manifest)
	if err != nil {
		return Plan{}, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err = tx.Exec(ctx, `INSERT INTO restore_plans
		(id,created_at,expires_at,actor_id,actor_username,file_sha256,blob_id,file_bytes,manifest,target_basis,before_archive_id,state,reason,blockers,cleared_tables)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, id, now, now.Add(10*time.Minute), actor.ID, actor.Username,
		upload.sum, upload.id, upload.size, metadata, targetBasis, beforeID, state, reason, blockers, cleared)
	if err != nil {
		return Plan{}, err
	}
	out, err := scanPlan(tx.QueryRow(ctx, `SELECT `+planColumns+` FROM restore_plans WHERE id=$1`, id))
	if err == nil {
		err = cleanExpiredUploads(ctx, tx)
	}
	if err == nil {
		err = s.finish(ctx, tx, token)
	}
	return out, err
}

func (s *Service) download(ctx context.Context, w http.ResponseWriter, token, id, planID string) error {
	tx, actor, err := s.begin(ctx, token)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var content, blobID *string
	var size int64
	var digest string
	err = tx.QueryRow(ctx, `SELECT file,blob_id,COALESCE(file_bytes,octet_length(file)),file_sha256 FROM backup_archives WHERE id=$1`, id).
		Scan(&content, &blobID, &size, &digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.ErrNotFound
	}
	if err != nil {
		return err
	}
	if size < 1 || !fingerprint.MatchString(digest) {
		return ErrInvalid
	}
	if planID != "" {
		var usable bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM restore_plans WHERE id=$1 AND actor_id=$2 AND before_archive_id=$3
			AND blob_id IS NOT NULL AND state='ready' AND expires_at>clock_timestamp())`, planID, actor.ID, id).Scan(&usable)
		if err != nil {
			return err
		}
		if !usable {
			return auth.ErrConflict
		}
	}
	var file io.Reader
	if blobID != nil {
		file = newBlobReader(ctx, tx, *blobID, size, digest)
	} else {
		if content == nil || hash([]byte(*content)) != digest {
			return ErrInvalid
		}
		file = strings.NewReader(*content)
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="sub-pool-`+id+`.spbak"`)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("X-Backup-SHA256", digest)
	w.WriteHeader(http.StatusOK)

	// Keep the last byte until both the stored size and the full file digest have been verified.
	if _, err = io.CopyN(w, file, size-1); err != nil {
		telemetry.Error(ctx, "backup_download", err)
		panic(http.ErrAbortHandler)
	}
	var last, extra [1]byte
	if _, err = io.ReadFull(file, last[:]); err == nil {
		if _, readErr := io.ReadFull(file, extra[:]); readErr != io.EOF {
			err = ErrInvalid
		}
	}
	if err == nil {
		_, err = w.Write(last[:])
	}
	if err == nil {
		err = http.NewResponseController(w).Flush()
	}
	if err != nil {
		telemetry.Error(ctx, "backup_download", err)
		panic(http.ErrAbortHandler)
	}
	if planID != "" && ctx.Err() == nil {
		_, err = tx.Exec(ctx, `UPDATE restore_plans SET before_download_proof=$4 WHERE id=$1 AND actor_id=$2 AND before_archive_id=$3
			AND state='ready' AND expires_at>clock_timestamp()`, planID, actor.ID, id, downloadedProof(token, planID, digest))
	}
	if err == nil {
		err = s.finish(ctx, tx, token)
	}
	if err != nil {
		telemetry.Error(ctx, "backup_download_receipt", err)
	}
	return nil
}
