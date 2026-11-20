package backups

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/database"
)

type ApplyInput struct {
	ID           string `json:"id"`
	TargetBasis  string `json:"target_basis"`
	Acknowledged bool   `json:"acknowledged"`
}

// Recover never replays a replacement whose previous process lost its final receipt.
func (s *Service) Recover(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `UPDATE restore_plans SET state='failed',reason='WORKER_INTERRUPTED',finished_at=clock_timestamp() WHERE state='applying' RETURNING id`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		task := "restore:" + id
		if err = journal(ctx, tx, nil, &task, "backup.restore", "failed", id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Service) Apply(ctx context.Context, token string, input ApplyInput) (Plan, error) {
	if !objectID.MatchString(input.ID) || !fingerprint.MatchString(input.TargetBasis) || !input.Acknowledged {
		return Plan{}, auth.ErrInput
	}
	var out Plan
	err := s.runtime.Exclusive(ctx, func(conn *pgxpool.Conn) error {
		session, err := s.auth.Current(ctx, token, false)
		if err != nil {
			return err
		}
		tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		actor, err := s.auth.AuthorizeAdminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		out, err = scanPlan(tx.QueryRow(ctx, `SELECT `+planColumns+` FROM restore_plans WHERE id=$1 FOR UPDATE`, input.ID))
		if err != nil {
			return err
		}
		if out.State != "ready" {
			return tx.Commit(ctx)
		}
		var owner, file string
		if err = tx.QueryRow(ctx, `SELECT actor_id,file FROM restore_plans WHERE id=$1`, input.ID).Scan(&owner, &file); err != nil {
			return err
		}
		if owner != actor.ID {
			return auth.ErrForbidden
		}
		if input.TargetBasis != out.TargetBasis {
			return auth.ErrConflict
		}
		expire := func(reason string) error {
			if _, err = tx.Exec(ctx, `UPDATE restore_plans SET state='expired',reason=$2,finished_at=clock_timestamp() WHERE id=$1`, input.ID, reason); err != nil {
				return err
			}
			out, err = scanPlan(tx.QueryRow(ctx, `SELECT `+planColumns+` FROM restore_plans WHERE id=$1`, input.ID))
			if err != nil {
				return err
			}
			if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
				return err
			}
			if err = journal(ctx, tx, &actor.ID, nil, "backup.restore", "conflict", input.ID); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
		if !time.Now().Before(out.ExpiresAt) {
			return expire("PLAN_EXPIRED")
		}
		if hash([]byte(file)) != out.FileSHA256 {
			return ErrInvalid
		}
		data, err := s.unprotect(file)
		if err != nil {
			return err
		}
		shape, err := readSchema(ctx, tx)
		if err != nil {
			return err
		}
		if err = validateRows(ctx, tx, shape, data); err != nil {
			return err
		}
		blockers, err := restoreBlockers(ctx, tx, shape, true)
		if err != nil {
			return err
		}
		if len(blockers) != 0 {
			return expire("BUSY_STATE_CHANGED")
		}
		current, err := capture(ctx, tx, shape, true)
		if err != nil {
			return err
		}
		if basis(current) != out.TargetBasis {
			return expire("BASIS_CHANGED")
		}
		var beforeFile, beforeHash string
		if out.BeforeArchiveID == nil {
			return ErrInvalid
		}
		if err = tx.QueryRow(ctx, `SELECT file,file_sha256 FROM backup_archives WHERE id=$1 AND kind='pre_restore'`, *out.BeforeArchiveID).Scan(&beforeFile, &beforeHash); err != nil {
			return ErrInvalid
		}
		before, err := s.unprotect(beforeFile)
		if err != nil || hash([]byte(beforeFile)) != beforeHash || !before.IncludeSecrets || basis(before) != out.TargetBasis {
			return ErrInvalid
		}
		if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE restore_plans SET state='applying',reason='APPLYING',started_at=clock_timestamp() WHERE id=$1`, input.ID); err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		if err = s.replace(ctx, conn, shape, data, token, input.ID, session); err != nil {
			// The predicate preserves a committed applied record even if its commit reply was lost.
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			reason := "RESTORE_FAILED"
			if errors.Is(err, auth.ErrUnauthorized) {
				reason = "AUTHORIZATION_EXPIRED"
			}
			failureTx, recordErr := s.pool.Begin(cleanup)
			if recordErr != nil {
				return recordErr
			}
			defer failureTx.Rollback(cleanup)
			changed, recordErr := failureTx.Exec(cleanup, `UPDATE restore_plans SET state='failed',reason=$2,finished_at=clock_timestamp() WHERE id=$1 AND state='applying'`, input.ID, reason)
			if recordErr != nil {
				return recordErr
			}
			if changed.RowsAffected() == 1 {
				task := "restore:" + input.ID
				if recordErr = journal(cleanup, failureTx, nil, &task, "backup.restore", "failed", input.ID); recordErr != nil {
					return recordErr
				}
			}
			if recordErr = failureTx.Commit(cleanup); recordErr != nil {
				return recordErr
			}
			out, err = scanPlan(s.pool.QueryRow(cleanup, `SELECT `+planColumns+` FROM restore_plans WHERE id=$1`, input.ID))
			return err
		}
		out, err = scanPlan(conn.QueryRow(ctx, `SELECT `+planColumns+` FROM restore_plans WHERE id=$1`, input.ID))
		return err
	})
	if errors.Is(err, database.ErrRuntimeBusy) {
		err = ErrBusy
	}
	if errors.Is(err, database.ErrRuntimeUnavailable) {
		err = &auth.Failure{Status: 503, Code: "RUNTIME_UNAVAILABLE"}
	}
	return out, err
}

func importOrder(ctx context.Context, tx pgx.Tx, shape schema) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT c.relname,r.relname FROM pg_constraint k JOIN pg_class c ON c.oid=k.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_class r ON r.oid=k.confrelid WHERE n.nspname=$1 AND k.contype='f' ORDER BY c.relname,r.relname`, shape.Namespace)
	if err != nil {
		return nil, err
	}
	dependencies := map[string][]string{}
	for rows.Next() {
		var name, parent string
		if err = rows.Scan(&name, &parent); err != nil {
			break
		}
		if !contains(businessTables, name) {
			continue
		}
		if !contains(businessTables, parent) {
			err = ErrSchema
			break
		}
		if parent != name {
			dependencies[name] = append(dependencies[name], parent)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []string{}
	seen := map[string]bool{}
	for len(result) < len(businessTables) {
		progress := false
		for _, name := range businessTables {
			if seen[name] {
				continue
			}
			ready := true
			for _, parent := range dependencies[name] {
				if !seen[parent] {
					ready = false
					break
				}
			}
			if ready {
				seen[name] = true
				result = append(result, name)
				progress = true
			}
		}
		if !progress {
			return nil, ErrSchema
		}
	}
	return result, nil
}

func (s *Service) replace(ctx context.Context, conn *pgxpool.Conn, shape schema, data snapshot, token, id string, session auth.Session) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return err
	}
	order, err := importOrder(ctx, tx, shape)
	if err != nil {
		return err
	}
	all := []string{shape.table("auth_sessions")}
	for _, name := range businessTables {
		all = append(all, shape.table(name))
	}
	if _, err = tx.Exec(ctx, `TRUNCATE TABLE `+strings.Join(all, ",")); err != nil {
		return err
	}
	byName := map[string]tableData{}
	for _, t := range data.Tables {
		byName[t.Name] = t
	}
	for _, name := range order {
		t := byName[name]
		body := rowsJSON(t.Rows)
		if _, err = tx.Exec(ctx, `INSERT INTO `+shape.table(name)+` (`+columnSQL(t.Columns)+`) OVERRIDING SYSTEM VALUE SELECT `+columnSQL(t.Columns)+` FROM json_populate_recordset(NULL::`+shape.table(name)+`,$1::json)`, body); err != nil {
			return err
		}
	}
	for _, seq := range data.Sequences {
		var name string
		for _, c := range shape.Tables[seq.Table].Columns {
			if c.Name == seq.Column && c.Sequence != nil {
				name = c.Sequence.Name
			}
		}
		if name == "" {
			return ErrSchema
		}
		// Native ALTER SEQUENCE is transactional; a failed replacement cannot consume the old sequence.
		if _, err = tx.Exec(ctx, `ALTER SEQUENCE `+ident(shape.Namespace, name)+` RESTART WITH `+seq.Next); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE connection_config SET revision=revision+1,diagnostic=NULL,reverification_required=(secret IS NOT NULL),saved_at=clock_timestamp(); UPDATE monitoring_config SET enabled=false,revision=revision+1,saved_at=clock_timestamp(); UPDATE notification_config SET enabled=false,financial_enabled=false,revision=revision+1,saved_at=clock_timestamp(); UPDATE monitoring_worker SET heartbeat_at=NULL; UPDATE auth_api_keys SET revoked_at=COALESCE(revoked_at,GREATEST(clock_timestamp(),created_at)),revision=revision+1; UPDATE maintenance_plans SET state='expired',reason='RESTORE_INVALIDATED',finished_at=clock_timestamp() WHERE state='ready'`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE wallet_automation SET enabled=false,revision=revision+1,updated_at=clock_timestamp()`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE pools SET weekly_wallet=weekly_wallet || jsonb_build_object('enabled',false,'auto_allocate',false,
	 'revision',((weekly_wallet->>'revision')::bigint+1)::text)
	 WHERE weekly_wallet->>'enabled'='true' OR weekly_wallet->>'auto_allocate'='true'`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE restore_plans SET state='expired',reason='TARGET_REPLACED',finished_at=clock_timestamp() WHERE state='ready' AND id<>$1`, id); err != nil {
		return err
	}
	task := "restore:" + id
	if err = journal(ctx, tx, nil, &task, "backup.restore", "succeeded", id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE restore_plans SET state='applied',reason='APPLIED',finished_at=clock_timestamp() WHERE id=$1 AND state='applying'`, id); err != nil {
		return err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	if !now.Before(session.IdleExpiresAt) || !now.Before(session.AbsoluteExpiresAt) {
		return auth.ErrUnauthorized
	}
	return tx.Commit(ctx)
}

func rowsJSON(rows []json.RawMessage) string { body, _ := json.Marshal(rows); return string(body) }

type Checks struct {
	PlanID           string     `json:"plan_id"`
	AppliedAt        time.Time  `json:"applied_at"`
	Source           Manifest   `json:"source"`
	Sessions         string     `json:"sessions"`
	ActiveAPIKeys    string     `json:"active_api_keys"`
	MonitoringPaused bool       `json:"monitoring_paused"`
	MailPaused       bool       `json:"mail_paused"`
	ConnectionState  string     `json:"connection_state"`
	RemoteEffects    string     `json:"remote_effects"`
	ReviewedAt       *time.Time `json:"reviewed_at"`
	ReviewedBy       *string    `json:"reviewed_by"`
}

func (s *Service) Checks(ctx context.Context, token, id string) (Checks, error) {
	if !objectID.MatchString(id) {
		return Checks{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token)
	if err != nil {
		return Checks{}, err
	}
	defer tx.Rollback(ctx)
	p, err := scanPlan(tx.QueryRow(ctx, `SELECT `+planColumns+` FROM restore_plans WHERE id=$1`, id))
	if err != nil {
		return Checks{}, err
	}
	if p.State != "applied" || p.FinishedAt == nil {
		return Checks{}, auth.ErrConflict
	}
	out := Checks{PlanID: id, AppliedAt: *p.FinishedAt, Source: p.Source, Sessions: "prior_sessions_removed", RemoteEffects: "not_replayed", ReviewedAt: p.ReviewedAt, ReviewedBy: p.ReviewedBy}
	var configured, requiresVerification bool
	var diagnostic []byte
	var revision string
	var source *string
	err = tx.QueryRow(ctx, `SELECT NOT m.enabled,NOT n.enabled,c.secret IS NOT NULL,c.reverification_required,(SELECT count(*)::text FROM auth_api_keys WHERE revoked_at IS NULL),c.diagnostic,c.revision::text,c.source_id FROM monitoring_config m CROSS JOIN notification_config n CROSS JOIN connection_config c`).Scan(&out.MonitoringPaused, &out.MailPaused, &configured, &requiresVerification, &out.ActiveAPIKeys, &diagnostic, &revision, &source)
	if err != nil {
		return Checks{}, err
	}
	out.ConnectionState = "not_configured"
	if configured {
		out.ConnectionState = "verification_required"
		var d struct {
			Revision string  `json:"revision"`
			SourceID *string `json:"source_id"`
			Saved    bool    `json:"saved"`
			Checks   []struct {
				ID    string `json:"id"`
				State string `json:"state"`
			} `json:"checks"`
		}
		// Restore and config saves clear diagnostics; only an explicit current-target test repopulates one.
		if !requiresVerification && json.Unmarshal(diagnostic, &d) == nil && d.Saved && d.Revision == revision && d.SourceID != nil && source != nil && *d.SourceID == *source {
			for _, c := range d.Checks {
				if c.ID == "management_auth" && c.State == "available" {
					out.ConnectionState = "verified_after_restore"
				}
			}
		}
	}
	if err = s.finish(ctx, tx, token); err != nil {
		return Checks{}, err
	}
	return out, nil
}

func (s *Service) Review(ctx context.Context, token, id string, acknowledged bool) (Plan, error) {
	if !objectID.MatchString(id) || !acknowledged {
		return Plan{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token)
	if err != nil {
		return Plan{}, err
	}
	defer tx.Rollback(ctx)
	p, err := scanPlan(tx.QueryRow(ctx, `SELECT `+planColumns+` FROM restore_plans WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return Plan{}, err
	}
	if p.State != "applied" {
		return Plan{}, auth.ErrConflict
	}
	if p.ReviewedAt == nil {
		if err = journal(ctx, tx, &actor.ID, nil, "backup.review", "succeeded", id); err != nil {
			return Plan{}, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE restore_plans SET reviewed_at=COALESCE(reviewed_at,clock_timestamp()),reviewed_by=COALESCE(reviewed_by,$2) WHERE id=$1`, id, actor.Username); err != nil {
		return Plan{}, err
	}
	out, err := scanPlan(tx.QueryRow(ctx, `SELECT `+planColumns+` FROM restore_plans WHERE id=$1`, id))
	if err == nil {
		err = s.finish(ctx, tx, token)
	}
	return out, err
}
