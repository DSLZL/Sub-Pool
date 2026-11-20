package backups

import (
	"context"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/database"
)

var objectID = regexp.MustCompile(`^[A-Za-z0-9_-]{21}[AQgw]$`)
var fingerprint = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Service struct {
	pool    *pgxpool.Pool
	auth    *auth.Service
	runtime *database.Runtime
	aead    cipher.AEAD
}

func New(pool *pgxpool.Pool, identity *auth.Service, runtime *database.Runtime, key []byte) (*Service, error) {
	if pool == nil || identity == nil || runtime == nil {
		return nil, errors.New("backup runtime exclusion is required")
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	return &Service{pool: pool, auth: identity, runtime: runtime, aead: aead}, nil
}

type Archive struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	CreatedAt     time.Time `json:"created_at"`
	ActorUsername string    `json:"actor_username"`
	FileSHA256    string    `json:"file_sha256"`
	Bytes         string    `json:"bytes"`
	Manifest
}
type File struct {
	Archive  Archive `json:"archive"`
	Filename string  `json:"filename"`
	Content  string  `json:"content"`
}
type Catalog struct {
	Version      string    `json:"version"`
	KeyAvailable bool      `json:"key_available"`
	Archives     []Archive `json:"archives"`
	NextAfter    *string   `json:"next_after"`
}
type Plan struct {
	ID              string     `json:"id"`
	CreatedAt       time.Time  `json:"created_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
	State           string     `json:"state"`
	Reason          string     `json:"reason"`
	FileSHA256      string     `json:"file_sha256"`
	TargetBasis     string     `json:"target_basis"`
	BeforeArchiveID *string    `json:"before_archive_id"`
	Blockers        []string   `json:"blockers"`
	StartedAt       *time.Time `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at"`
	ReviewedAt      *time.Time `json:"reviewed_at"`
	ReviewedBy      *string    `json:"reviewed_by"`
	Source          Manifest   `json:"source"`
}

const archiveColumns = `id,kind,created_at,actor_username,file_sha256,octet_length(file)::text,manifest`
const planColumns = `id,created_at,expires_at,state,reason,file_sha256,target_basis,before_archive_id,blockers,started_at,finished_at,reviewed_at,reviewed_by,manifest`

func scanArchive(row pgx.Row) (Archive, error) {
	var out Archive
	var body []byte
	err := row.Scan(&out.ID, &out.Kind, &out.CreatedAt, &out.ActorUsername, &out.FileSHA256, &out.Bytes, &body)
	if err == nil {
		err = json.Unmarshal(body, &out.Manifest)
	}
	out.CreatedAt = out.CreatedAt.UTC()
	out.SourceCutoff = out.SourceCutoff.UTC()
	return out, err
}
func scanPlan(row pgx.Row) (Plan, error) {
	var out Plan
	var body []byte
	err := row.Scan(&out.ID, &out.CreatedAt, &out.ExpiresAt, &out.State, &out.Reason, &out.FileSHA256, &out.TargetBasis, &out.BeforeArchiveID, &out.Blockers, &out.StartedAt, &out.FinishedAt, &out.ReviewedAt, &out.ReviewedBy, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		err = auth.ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal(body, &out.Source)
	}
	out.CreatedAt = out.CreatedAt.UTC()
	out.ExpiresAt = out.ExpiresAt.UTC()
	out.Source.SourceCutoff = out.Source.SourceCutoff.UTC()
	for _, at := range []*time.Time{out.StartedAt, out.FinishedAt, out.ReviewedAt} {
		if at != nil {
			*at = at.UTC()
		}
	}
	return out, err
}

func (s *Service) begin(ctx context.Context, token string) (pgx.Tx, auth.Identity, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, auth.Identity{}, err
	}
	actor, err := s.auth.AuthorizeAdminSession(ctx, tx, token)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, actor, err
	}
	return tx, actor, nil
}
func (s *Service) finish(ctx context.Context, tx pgx.Tx, token string) error {
	if _, err := s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func journal(ctx context.Context, tx pgx.Tx, actor *string, task *string, action, outcome, id string) error {
	_, err := audit.Record(ctx, tx, audit.Entry{ActorID: actor, TaskID: task, Action: action, Outcome: outcome, TargetType: "backup", TargetID: id})
	return err
}

func (s *Service) saveArchive(ctx context.Context, tx pgx.Tx, actor auth.Identity, id, kind string, data snapshot) (Archive, error) {
	file, err := s.protect(data)
	if err != nil {
		return Archive{}, err
	}
	body, err := json.Marshal(manifest(data))
	if err != nil {
		return Archive{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO backup_archives(id,kind,created_at,actor_id,actor_username,include_secrets,source_cutoff,file_sha256,file,manifest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, kind, time.Now().UTC(), actor.ID, actor.Username, data.IncludeSecrets, data.CreatedAt, hash([]byte(file)), file, body)
	if err != nil {
		return Archive{}, err
	}
	return scanArchive(tx.QueryRow(ctx, `SELECT `+archiveColumns+` FROM backup_archives WHERE id=$1`, id))
}

func (s *Service) Export(ctx context.Context, token, id string, includeSecrets bool) (Archive, error) {
	if !objectID.MatchString(id) {
		return Archive{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token)
	if err != nil {
		return Archive{}, err
	}
	defer tx.Rollback(ctx)
	// Serialize the caller's known operation key without treating an ambiguous write as a new export.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,771))`, id); err != nil {
		return Archive{}, err
	}
	var owner, kind string
	var previousSecrets bool
	err = tx.QueryRow(ctx, `SELECT actor_id,kind,include_secrets FROM backup_archives WHERE id=$1`, id).Scan(&owner, &kind, &previousSecrets)
	if err == nil {
		if owner != actor.ID || kind != "export" || previousSecrets != includeSecrets {
			return Archive{}, auth.ErrConflict
		}
		out, e := scanArchive(tx.QueryRow(ctx, `SELECT `+archiveColumns+` FROM backup_archives WHERE id=$1`, id))
		if e == nil {
			e = s.finish(ctx, tx, token)
		}
		return out, e
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Archive{}, err
	}
	shape, err := readSchema(ctx, tx)
	if err != nil {
		return Archive{}, err
	}
	data, err := capture(ctx, tx, shape, includeSecrets)
	if err != nil {
		return Archive{}, err
	}
	out, err := s.saveArchive(ctx, tx, actor, id, "export", data)
	if err == nil {
		err = journal(ctx, tx, &actor.ID, nil, "backup.export", "succeeded", id)
	}
	if err == nil {
		err = s.finish(ctx, tx, token)
	}
	return out, err
}

func (s *Service) GetFile(ctx context.Context, token, id string) (File, error) {
	if !objectID.MatchString(id) {
		return File{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token)
	if err != nil {
		return File{}, err
	}
	defer tx.Rollback(ctx)
	out, err := scanArchive(tx.QueryRow(ctx, `SELECT `+archiveColumns+` FROM backup_archives WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return File{}, auth.ErrNotFound
	}
	if err != nil {
		return File{}, err
	}
	var content string
	if err = tx.QueryRow(ctx, `SELECT file FROM backup_archives WHERE id=$1`, id).Scan(&content); err != nil {
		return File{}, err
	}
	if hash([]byte(content)) != out.FileSHA256 {
		return File{}, ErrInvalid
	}
	if err = s.finish(ctx, tx, token); err != nil {
		return File{}, err
	}
	return File{Archive: out, Filename: "sub-pool-" + id + ".spbak", Content: content}, nil
}

func (s *Service) List(ctx context.Context, token, after string, limit int) (Catalog, error) {
	out := Catalog{Version: "backup-v1", KeyAvailable: s.aead != nil, Archives: []Archive{}}
	if after != "" && !objectID.MatchString(after) || limit < 1 || limit > 100 {
		return out, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if after != "" {
		var found bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM backup_archives WHERE id=$1)`, after).Scan(&found); err != nil {
			return out, err
		}
		if !found {
			return out, auth.ErrInput
		}
	}
	rows, err := tx.Query(ctx, `SELECT `+archiveColumns+` FROM backup_archives WHERE $1='' OR (created_at,id)<(SELECT created_at,id FROM backup_archives WHERE id=$1) ORDER BY created_at DESC,id DESC LIMIT $2`, after, limit+1)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		item, e := scanArchive(rows)
		if e != nil {
			err = e
			break
		}
		out.Archives = append(out.Archives, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Archives) > limit {
		out.Archives = out.Archives[:limit]
		id := out.Archives[limit-1].ID
		out.NextAfter = &id
	}
	err = s.finish(ctx, tx, token)
	return out, err
}

func (s *Service) Preflight(ctx context.Context, token, id, file string) (Plan, error) {
	if !objectID.MatchString(id) {
		return Plan{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token)
	if err != nil {
		return Plan{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,772))`, id); err != nil {
		return Plan{}, err
	}
	var owner, previousHash string
	err = tx.QueryRow(ctx, `SELECT actor_id,file_sha256 FROM restore_plans WHERE id=$1`, id).Scan(&owner, &previousHash)
	if err == nil {
		if owner != actor.ID || previousHash != hash([]byte(file)) {
			return Plan{}, auth.ErrConflict
		}
		out, e := scanPlan(tx.QueryRow(ctx, `SELECT `+planColumns+` FROM restore_plans WHERE id=$1`, id))
		if e == nil {
			e = s.finish(ctx, tx, token)
		}
		return projectExpiry(out), e
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Plan{}, err
	}
	data, err := s.unprotect(file)
	if err != nil {
		return Plan{}, err
	}
	shape, err := readSchema(ctx, tx)
	if err != nil {
		return Plan{}, err
	}
	if err = validateRows(ctx, tx, shape, data); err != nil {
		return Plan{}, err
	}
	blockers, err := restoreBlockers(ctx, tx, shape, true)
	if err != nil {
		return Plan{}, err
	}
	state, reason, outcome := "ready", "READY", "succeeded"
	if len(blockers) > 0 {
		state, reason, outcome = "blocked", "RESTORE_BLOCKED", "denied"
	}
	if err = journal(ctx, tx, &actor.ID, nil, "backup.preflight", outcome, id); err != nil {
		return Plan{}, err
	}
	current, err := capture(ctx, tx, shape, true)
	if err != nil {
		return Plan{}, err
	}
	var before *string
	if state == "ready" {
		beforeID, e := newID()
		if e != nil {
			return Plan{}, e
		}
		if _, e = s.saveArchive(ctx, tx, actor, beforeID, "pre_restore", current); e != nil {
			return Plan{}, e
		}
		before = &beforeID
	}
	body, err := json.Marshal(manifest(data))
	if err != nil {
		return Plan{}, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err = tx.Exec(ctx, `INSERT INTO restore_plans(id,created_at,expires_at,actor_id,actor_username,file_sha256,file,manifest,target_basis,before_archive_id,state,reason,blockers) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, id, now, now.Add(10*time.Minute), actor.ID, actor.Username, hash([]byte(file)), file, body, basis(current), before, state, reason, blockers)
	if err != nil {
		return Plan{}, err
	}
	out, err := scanPlan(tx.QueryRow(ctx, `SELECT `+planColumns+` FROM restore_plans WHERE id=$1`, id))
	if err == nil {
		err = s.finish(ctx, tx, token)
	}
	return out, err
}

func projectExpiry(out Plan) Plan {
	if out.State == "ready" && !time.Now().Before(out.ExpiresAt) {
		out.State = "expired"
		out.Reason = "PLAN_EXPIRED"
		at := out.ExpiresAt
		out.FinishedAt = &at
	}
	return out
}
func (s *Service) GetPlan(ctx context.Context, token, id string) (Plan, error) {
	if !objectID.MatchString(id) {
		return Plan{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token)
	if err != nil {
		return Plan{}, err
	}
	defer tx.Rollback(ctx)
	out, err := scanPlan(tx.QueryRow(ctx, `SELECT `+planColumns+` FROM restore_plans WHERE id=$1`, id))
	if err == nil {
		err = s.finish(ctx, tx, token)
	}
	return projectExpiry(out), err
}

func restoreBlockers(ctx context.Context, tx pgx.Tx, shape schema, includeCandidate bool) ([]string, error) {
	out := []string{}
	addReason := func(reason string) {
		for _, existing := range out {
			if existing == reason {
				return
			}
		}
		out = append(out, reason)
	}
	queries := []struct{ table, where, reason string }{
		{"collection_jobs", `state IN ('queued','running')`, "COLLECTION_ACTIVE"},
		{"notifications", `state IN ('queued','dispatching')`, "MAIL_ACTIVE"},
		{"maintenance_plans", `state IN ('queued','running')`, "MAINTENANCE_ACTIVE"},
		{"dispatch_pauses", `state IN ('pausing','paused','restoring','needs_review')`, "REMOTE_WRITE_UNCONFIRMED"},
		{"pricing_changes", `state IN ('applying','applied','reverting','needs_review')`, "REMOTE_WRITE_UNCONFIRMED"},
	}
	for _, q := range queries {
		var found bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+shape.table(q.table)+` WHERE `+q.where+`)`).Scan(&found); err != nil {
			return nil, err
		}
		if found {
			addReason(q.reason)
		}
		if includeCandidate {
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+tempTable(q.table)+` WHERE `+q.where+`)`).Scan(&found); err != nil {
				return nil, err
			}
			if found {
				addReason("BACKUP_" + q.reason)
			}
		}
	}
	wallets := []struct{ requests, results, prefix string }{{shape.table("wallet_application_requests"), shape.table("wallet_application_results"), ""}}
	if includeCandidate {
		wallets = append(wallets, struct{ requests, results, prefix string }{tempTable("wallet_application_requests"), tempTable("wallet_application_results"), "BACKUP_"})
	}
	for _, tables := range wallets {
		var pending bool
		query := `SELECT EXISTS(SELECT 1 FROM ` + tables.requests + ` r WHERE NOT (COALESCE((SELECT x.resolved AND (
			(r.snapshot->>'unit'='simulated_wallet_usd' AND r.snapshot->'simulation'=x.snapshot->'simulation')
			OR (r.snapshot->>'unit'='native_wallet' AND x.snapshot->>'unit'='native_wallet'
				AND r.snapshot #>> '{details,input,native_delta}' IS NOT NULL
				AND r.snapshot #> '{details,input}'=x.snapshot #> '{details,input}')
		) FROM ` + tables.results + ` x WHERE x.application_id=r.id ORDER BY x.id DESC LIMIT 1),r.snapshot->>'state'='rejected' AND r.snapshot->>'dispatch_state'='not_dispatched') IS TRUE))`
		if err := tx.QueryRow(ctx, query).Scan(&pending); err != nil {
			return nil, err
		}
		if pending {
			addReason(tables.prefix + "REMOTE_WRITE_UNCONFIRMED")
		}
	}
	if includeCandidate {
		var historyChanged bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+shape.table("weekly_wallet_allocations")+` w
		 WHERE NOT EXISTS (SELECT 1 FROM `+tempTable("weekly_wallet_allocations")+` b WHERE b.id=w.id AND to_jsonb(b)=to_jsonb(w))
		 OR EXISTS(SELECT 1 FROM `+shape.table("wallet_application_requests")+` r WHERE r.id=w.application_id AND NOT EXISTS
		    (SELECT 1 FROM `+tempTable("wallet_application_requests")+` b WHERE b.id=r.id AND to_jsonb(b)=to_jsonb(r)))
		 OR EXISTS(SELECT 1 FROM `+shape.table("wallet_application_results")+` r WHERE r.application_id=w.application_id AND NOT EXISTS
		    (SELECT 1 FROM `+tempTable("wallet_application_results")+` b WHERE b.id=r.id AND to_jsonb(b)=to_jsonb(r))))`).Scan(&historyChanged); err != nil {
			return nil, err
		}
		if historyChanged {
			addReason("WEEKLY_WALLET_HISTORY_CHANGED")
		}
		if source := auth.ObservedSource(ctx); source.IP != nil {
			var blocked bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+tempTable("source_blocks")+` WHERE ip=$1::inet AND blocked)`, *source.IP).Scan(&blocked); err != nil {
				return nil, err
			}
			if blocked {
				out = append(out, "BACKUP_SOURCE_BLOCKED")
			}
		}
	}
	return out, nil
}
