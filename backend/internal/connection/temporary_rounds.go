package connection

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
)

type TemporaryCap struct {
	AccountID     string `json:"account_id"`
	ParticipantID string `json:"participant_id"`
	Amount        string `json:"amount"`
}
type TemporaryStart struct {
	RequestID                string         `json:"request_id"`
	ScopeHash                string         `json:"scope_hash"`
	Mode                     string         `json:"mode"`
	Deadline                 time.Time      `json:"deadline"`
	Caps                     []TemporaryCap `json:"caps"`
	AcknowledgedParticipants []string       `json:"acknowledged_participants"`
	Informed                 bool           `json:"informed"`
	Confirmed                bool           `json:"confirmed"`
}
type TemporaryExit struct {
	ExitedAt   time.Time `json:"exited_at"`
	Reason     string    `json:"reason"`
	RecordedAt time.Time `json:"recorded_at"`
	ActorID    *string   `json:"actor_id"`
}
type TemporaryRound struct {
	ID                       string         `json:"id"`
	ActorID                  string         `json:"actor_id"`
	StartedAt                time.Time      `json:"started_at"`
	Deadline                 time.Time      `json:"deadline"`
	Mode                     string         `json:"mode"`
	Scope                    TemporaryScope `json:"scope"`
	Caps                     []TemporaryCap `json:"caps"`
	AcknowledgedParticipants []string       `json:"acknowledged_participants"`
	Informed                 bool           `json:"informed"`
	Exit                     *TemporaryExit `json:"exit"`
}
type temporarySaved struct {
	Mode                     string         `json:"mode"`
	Scope                    TemporaryScope `json:"scope"`
	Caps                     []TemporaryCap `json:"caps"`
	AcknowledgedParticipants []string       `json:"acknowledged_participants"`
	Informed                 bool           `json:"informed"`
}

func temporaryRound(ctx context.Context, tx pgx.Tx, id string) (TemporaryRound, error) {
	out := TemporaryRound{}
	var raw, exit []byte
	err := tx.QueryRow(ctx, `SELECT r.id,r.actor_id,r.started_at,r.deadline,r.snapshot,
 (SELECT jsonb_build_object('exited_at',e.exited_at,'reason',e.reason,'recorded_at',e.recorded_at,'actor_id',e.actor_id) FROM temporary_exits e WHERE e.round_id=r.id)
 FROM temporary_rounds r WHERE r.id=$1`, id).Scan(&out.ID, &out.ActorID, &out.StartedAt, &out.Deadline, &raw, &exit)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, auth.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	var saved temporarySaved
	if err = json.Unmarshal(raw, &saved); err != nil {
		return out, err
	}
	out.Mode, out.Scope, out.Caps, out.AcknowledgedParticipants, out.Informed = saved.Mode, saved.Scope, saved.Caps, saved.AcknowledgedParticipants, saved.Informed
	if exit != nil {
		out.Exit = &TemporaryExit{}
		err = json.Unmarshal(exit, out.Exit)
		out.Exit.ExitedAt, out.Exit.RecordedAt = out.Exit.ExitedAt.UTC(), out.Exit.RecordedAt.UTC()
	}
	out.StartedAt, out.Deadline = out.StartedAt.UTC(), out.Deadline.UTC()
	return out, err
}

func temporaryLatest(ctx context.Context, tx pgx.Tx) (*TemporaryRound, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM temporary_rounds ORDER BY started_at DESC,id DESC LIMIT 1`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out, err := temporaryRound(ctx, tx, id)
	return &out, err
}

// The logical exit is independent of worker liveness and is never later than the frozen deadline.
func temporaryEffectiveExit(ctx context.Context, tx pgx.Tx, r TemporaryRound, now time.Time) (*TemporaryExit, error) {
	if r.Exit != nil {
		return r.Exit, nil
	}
	at, reason := r.Deadline, "deadline"
	for _, a := range r.Scope.Accounts {
		rows, err := tx.Query(ctx, `SELECT snapshot FROM quota_observations WHERE account_id=$1
		 AND (snapshot->>'source_at')::timestamptz >= $2
		 AND (snapshot->>'source_at')::timestamptz <= $3
		 AND (snapshot->>'source_at')::timestamptz < $4
		 ORDER BY (snapshot->>'source_at')::timestamptz,id`, a.AccountID, r.StartedAt, now, at)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var raw []byte
			var snapshot QuotaSnapshot
			if err = rows.Scan(&raw); err == nil {
				err = json.Unmarshal(raw, &snapshot)
			}
			if err != nil {
				break
			}
			if snapshot.Simulation == nil || *snapshot.Simulation != r.Scope.Simulation || snapshot.SourceID != r.Scope.SourceID || snapshot.AccountRevision != a.AccountRevision || snapshot.SourceAt == nil || snapshot.SourceAt.Before(r.StartedAt) || snapshot.SourceAt.After(now) {
				continue
			}
			for _, frozen := range a.Periods {
				period := nativeObservationPeriod(snapshot, frozen.Kind)
				if period != nil && (!period.StartAt.Equal(frozen.StartAt) || !period.EndAt.Equal(frozen.EndAt) || period.DurationSeconds != frozen.DurationSeconds) && snapshot.SourceAt.Before(at) {
					at, reason = *snapshot.SourceAt, "period_changed"
				}
			}
			if reason == "period_changed" && at.Equal(*snapshot.SourceAt) {
				break
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if now.Before(at) {
		return nil, nil
	}
	return &TemporaryExit{ExitedAt: at.UTC(), Reason: reason, RecordedAt: now.UTC()}, nil
}

func temporaryPersistExit(ctx context.Context, tx pgx.Tx, r *TemporaryRound, exit *TemporaryExit) error {
	if exit == nil || r.Exit != nil {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO temporary_exits(round_id,exited_at,reason,actor_id,recorded_at) VALUES($1,$2,$3,$4,$5)`, r.ID, exit.ExitedAt, exit.Reason, exit.ActorID, exit.RecordedAt); err != nil {
		return err
	}
	task := "temporary:" + r.ID
	entry := audit.Entry{ActorID: exit.ActorID, Action: "temporary.exit", Outcome: "succeeded", TargetType: "temporary_round", TargetID: r.ID}
	if exit.ActorID == nil {
		entry.TaskID = &task
	}
	_, err := audit.Record(ctx, tx, entry)
	if err == nil {
		r.Exit = exit
	}
	return err
}

func validateTemporaryStart(scope TemporaryScope, in TemporaryStart, now time.Time) bool {
	if !managedID(in.RequestID) || in.ScopeHash != temporaryHash(scope) || !in.Confirmed || in.Mode != "carry" && in.Mode != "no_carry" || in.Mode == "no_carry" && !in.Informed || !now.Before(in.Deadline) || in.Deadline.After(scope.LatestDeadline) || !now.Before(scope.ExpiresAt) || len(in.Caps) > 500*500 {
		return false
	}
	ids := []string{}
	for _, w := range scope.Wallets {
		ids = append(ids, w.ParticipantID)
	}
	if !simulationWalletAccounts(ids, in.AcknowledgedParticipants) {
		return false
	}
	caps := map[string]*big.Rat{}
	wallets := map[string]*big.Rat{}
	for _, cap := range in.Caps {
		amount, ok := simulationDecimal(cap.Amount)
		key := cap.AccountID + ":" + cap.ParticipantID
		if !ok || caps[key] != nil {
			return false
		}
		caps[key] = amount
	}
	count := 0
	for _, a := range scope.Accounts {
		total := new(big.Rat)
		for _, m := range a.Members {
			cap := caps[a.AccountID+":"+m.ParticipantID]
			if cap == nil {
				return false
			}
			total.Add(total, cap)
			count++
			if wallets[m.ParticipantID] == nil {
				wallets[m.ParticipantID] = new(big.Rat)
			}
			wallets[m.ParticipantID].Add(wallets[m.ParticipantID], cap)
		}
		remaining, ok := a.Remaining.Rational()
		if !ok || total.Cmp(remaining) > 0 {
			return false
		}
	}
	if count != len(in.Caps) {
		return false
	}
	for _, w := range scope.Wallets {
		if wallets[w.ParticipantID] == nil || wallets[w.ParticipantID].Sign() <= 0 {
			return false
		}
	}
	return true
}

func (s *Service) StartTemporary(ctx context.Context, token string, in TemporaryStart) (TemporaryRound, error) {
	if !managedID(in.RequestID) {
		return TemporaryRound{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return TemporaryRound{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, temporaryLock); err != nil {
		return TemporaryRound{}, err
	}
	digest := temporaryHash(in)
	var existing, previous string
	err = tx.QueryRow(ctx, `SELECT id,request_digest FROM temporary_rounds WHERE actor_id=$1 AND request_id=$2`, actor.ID, in.RequestID).Scan(&existing, &previous)
	if err == nil {
		if previous != digest {
			return TemporaryRound{}, auth.ErrConflict
		}
		out, e := temporaryRound(ctx, tx, existing)
		if e == nil {
			e = s.finish(ctx, tx, token, true)
		}
		return out, e
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return TemporaryRound{}, err
	}
	old, err := temporaryLatest(ctx, tx)
	if err != nil {
		return TemporaryRound{}, err
	}
	var previousExit *TemporaryExit
	if old != nil {
		exit, err := temporaryEffectiveExit(ctx, tx, *old, time.Now().UTC())
		if err != nil {
			return TemporaryRound{}, err
		}
		if exit == nil {
			return TemporaryRound{}, auth.ErrConflict
		}
		previousExit = exit
		if old.Mode == "carry" && exit.Reason != "terminated" {
			ready, err := temporarySettlementsFinished(ctx, tx, *old)
			if err != nil {
				return TemporaryRound{}, err
			}
			if !ready {
				return TemporaryRound{}, auth.ErrConflict
			}
		}
	}
	preflight, err := s.temporaryPreflight(ctx, tx)
	if err != nil {
		return TemporaryRound{}, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if preflight.Scope == nil || !validateTemporaryStart(*preflight.Scope, in, now) {
		return TemporaryRound{}, auth.ErrConflict
	}
	if old != nil {
		if err = temporaryPersistExit(ctx, tx, old, previousExit); err != nil {
			return TemporaryRound{}, err
		}
	}
	if old != nil && old.Exit != nil && (old.Mode == "no_carry" || old.Exit.Reason == "terminated") {
		for _, account := range old.Scope.Accounts {
			settlement, err := s.temporarySettleAccount(ctx, tx, storedConfig{}, *old, account)
			if err != nil {
				return TemporaryRound{}, err
			}
			if err = temporarySaveSettlement(ctx, tx, settlement); err != nil {
				return TemporaryRound{}, err
			}
		}
	}
	sum := sha256.Sum256([]byte("temporary:" + actor.ID + ":" + in.RequestID))
	id := base64.RawURLEncoding.EncodeToString(sum[:16])
	saved := temporarySaved{Mode: in.Mode, Scope: *preflight.Scope, Caps: in.Caps, AcknowledgedParticipants: in.AcknowledgedParticipants, Informed: in.Informed}
	raw, err := json.Marshal(saved)
	if err != nil {
		return TemporaryRound{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO temporary_rounds(id,actor_id,actor_revision,request_id,request_digest,started_at,deadline,snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, actor.ID, actor.Revision, in.RequestID, digest, now, in.Deadline, raw); err != nil {
		return TemporaryRound{}, err
	}
	_, err = audit.Record(ctx, tx, audit.Entry{ActorID: &actor.ID, Action: "temporary.start", Outcome: "succeeded", TargetType: "temporary_round", TargetID: id})
	out := TemporaryRound{}
	if err == nil {
		out, err = temporaryRound(ctx, tx, id)
	}
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return TemporaryRound{}, err
	}
	return out, nil
}

func (s *Service) StopTemporary(ctx context.Context, token, id string) (TemporaryRound, error) {
	if !managedID(id) {
		return TemporaryRound{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return TemporaryRound{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, temporaryLock); err != nil {
		return TemporaryRound{}, err
	}
	out, err := temporaryRound(ctx, tx, id)
	if err != nil {
		return TemporaryRound{}, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	exit, err := temporaryEffectiveExit(ctx, tx, out, now)
	if err != nil {
		return TemporaryRound{}, err
	}
	if exit == nil {
		exit = &TemporaryExit{ExitedAt: now, RecordedAt: now, Reason: "terminated", ActorID: &actor.ID}
	}
	if err = temporaryPersistExit(ctx, tx, &out, exit); err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return TemporaryRound{}, err
	}
	return out, nil
}

func temporaryAccountMatches(a TemporaryAccount, b walletBasis, current walletSource) bool {
	if current.Contract == nil || current.Estimate == nil {
		return false
	}
	return current.AccountID == a.AccountID && current.UpstreamID == a.UpstreamID && current.Revision == a.AccountRevision && current.Contract.ID == a.ContractID && current.ContractHead == a.ContractHead && current.MemberHead == a.MemberHead && current.EstimateRevision == a.EstimateRevision && current.ManagementRevision == a.ManagementRevision && current.Contract.Enabled && current.Contract.SourceID == b.SourceID
}

func temporarySamePeriod(a, b ObservationPeriod) bool {
	return a.Kind == b.Kind && a.StartAt.Equal(b.StartAt) && a.EndAt.Equal(b.EndAt) && a.DurationSeconds == b.DurationSeconds && a.Basis == b.Basis
}

func temporaryPeriodsMatch(frozen []ObservationPeriod, current []EstimateWindow) bool {
	if len(frozen) != len(current) {
		return false
	}
	for _, a := range frozen {
		found := false
		for _, b := range current {
			if b.Period != nil && temporarySamePeriod(a, *b.Period) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func temporaryWalletBasisHead(ctx context.Context, tx pgx.Tx, b *walletBasis) error {
	r, err := temporaryLatest(ctx, tx)
	if err != nil || r == nil {
		return err
	}
	relevant := false
	for _, w := range r.Scope.Wallets {
		if w.BindingID == b.BindingID {
			relevant = true
		}
	}
	if !relevant {
		return nil
	}
	exit, err := temporaryEffectiveExit(ctx, tx, *r, time.Now().UTC())
	if err != nil || exit == nil {
		return err
	}
	head := exit.ExitedAt.Format(time.RFC3339Nano) + ":" + exit.Reason
	b.TemporaryHead += ":" + head
	for i := range b.Sources {
		for _, a := range r.Scope.Accounts {
			if b.Sources[i].AccountID == a.AccountID {
				b.Sources[i].TemporaryHead += ":" + head
			}
		}
	}
	return nil
}
