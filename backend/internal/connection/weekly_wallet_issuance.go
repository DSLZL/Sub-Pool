package connection

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
)

func pendingWeeklyIssuance(ctx context.Context, tx pgx.Tx, member Participant) (string, bool, error) {
	var id string
	var weekly bool
	err := tx.QueryRow(ctx, `SELECT r.id::text,
	 (EXISTS(SELECT 1 FROM weekly_wallet_issuances w WHERE w.application_id=r.id)
	  OR EXISTS(SELECT 1 FROM weekly_wallet_allocations w WHERE w.application_id=r.id)
	  OR EXISTS(SELECT 1 FROM weekly_wallet_groups w WHERE w.application_id=r.id))
	 FROM wallet_application_requests r JOIN participant_bindings b ON b.id=r.binding_id
	 WHERE b.source_id=$1 AND b.upstream_user_id=$2::text::bigint AND r.snapshot->>'unit'='native_wallet'
	  AND NOT (`+walletApplicationResolvedSQL+`)
	 ORDER BY r.id DESC LIMIT 1`, member.Binding.SourceID, member.Binding.UpstreamUserID).Scan(&id, &weekly)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return id, weekly, err
}

func (s *Service) weeklyIssuanceRecommendation(ctx context.Context, tx pgx.Tx, token string, out WeeklyWalletRecommendation) (WeeklyWalletRecommendation, error) {
	manual := &weeklyWalletManual{token: token, preview: true}
	env, ok, err := s.weeklyWalletGroupEnvironment(ctx, tx, out.ParticipantID, manual)
	if err != nil || !ok {
		return out, err
	}
	out.Pool = &env.pools[0]
	id, _, err := pendingWeeklyIssuance(ctx, tx, env.member)
	if err != nil {
		return out, err
	}
	if id != "" {
		out.State = "pending"
		return out, nil
	}
	entry, issues, _, _, err := s.weeklyIssuanceCandidate(ctx, tx, env, out.Pool.ID, manual)
	if err != nil {
		return out, err
	}
	out.State, out.Issues = entry.State, issues
	if entry.Issuance != nil {
		kind, secret, err := s.decrypt(env.config)
		if err != nil {
			return out, err
		}
		entry.Issuance.Balance, entry.Issuance.Frozen = s.nativeWalletFields(ctx, env.config, kind, secret, env.member.Binding.UpstreamUserID)
		out.Proposal = &weeklyWalletProposal{PeriodEnd: entry.PeriodEnd, Delta: entry.Issuance.Amount, Issuance: entry.Issuance}
		if entry.basis != "" {
			out.Proposal.Basis = &entry.basis
		}
	} else if entry.State == "waiting_estimate" || entry.State == "waiting_actual_cost" {
		out.Reference, err = s.readWeeklyWalletReference(ctx, tx, env.config, env.policy, env.member, env.pools)
	}
	return out, err
}

func (s *Service) applyWeeklyIssuance(ctx context.Context, pool, participant string, manual *weeklyWalletManual) (weeklyWalletEntry, error) {
	var entry weeklyWalletEntry
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return entry, err
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return entry, err
	}
	defer tx.Rollback(ctx)
	if err = lockWallet(ctx, tx, participant); err != nil {
		return entry, err
	}
	env, active, err := s.weeklyWalletGroupEnvironment(ctx, tx, participant, manual)
	if err != nil {
		return entry, err
	}
	if !active {
		return entry, weeklyIssuanceConflict(manual)
	}
	wallet := nativeWalletLockKey(env.member.Binding.SourceID, env.member.Binding.UpstreamUserID)
	if err = lockWallet(ctx, tx, wallet); err != nil {
		return entry, err
	}
	id, weekly, err := pendingWeeklyIssuance(ctx, tx, env.member)
	if err != nil {
		return entry, err
	}
	if id != "" {
		if manual != nil {
			return entry, auth.ErrConflict
		}
		if !weekly {
			return entry, nil
		}
		return entry, s.recoverWeeklyIssuance(ctx, tx, env.config, env.member, id)
	}
	entry, _, actor, grant, err := s.weeklyIssuanceCandidate(ctx, tx, env, pool, manual)
	if err != nil {
		return entry, err
	}
	if entry.State != "ready" || entry.basis == "" || manual != nil && entry.basis != manual.basis {
		return entry, weeklyIssuanceConflict(manual)
	}
	c := env.config
	var intent *WalletApplication
	entry.State = "applied"
	if entry.Issuance.Amount != "0" {
		kind, secret, err := s.decrypt(c)
		if err != nil {
			return entry, err
		}
		before, frozen, verified := s.nativeWalletUserFields(ctx, c, kind, secret, env.member.Binding.UpstreamUserID)
		if !verified || !s.weeklyWalletWriteAvailable(ctx, c, kind, secret, true) {
			return entry, weeklyIssuanceConflict(manual)
		}
		entry.Issuance.Balance, entry.Issuance.Frozen = before, frozen
		out, err := makeWeeklyIssuanceIntent(ctx, tx, actor, c, env.member, entry)
		if err != nil {
			return entry, err
		}
		entry.ApplicationID, intent, entry.State = &out.ID, &out, "pending"
	}
	if err = saveWeeklyIssuance(ctx, tx, env.member, &entry); err != nil {
		return entry, err
	}
	task, outcome := "weekly_wallet", "succeeded"
	if intent != nil {
		outcome = "pending"
	}
	event := audit.Entry{
		TaskID: &task, OperationID: "weekly-wallet-issuance:" + entry.ID, Action: "pool.weekly_wallet." + entry.Phase,
		Outcome: outcome, TargetType: "participant", TargetID: participant, SourceID: c.SourceID,
	}
	if manual != nil {
		event.ActorID, event.TaskID = &actor, nil
	}
	if _, err = audit.Record(ctx, tx, event); err != nil {
		return entry, err
	}
	if intent == nil {
		if manual != nil {
			if _, err = s.auth.AuthorizeAdminSession(ctx, tx, manual.token); err != nil {
				return entry, err
			}
		}
		return entry, tx.Commit(ctx)
	}
	for _, lock := range []string{participant, wallet} {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 1380991564))`, lock); err != nil {
			return entry, err
		}
		defer unlockWalletSession(conn, lock)
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_lock_shared($1)`, temporaryLock); err != nil {
		return entry, err
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 2*time.Second)
		defer done()
		if _, err := conn.Exec(cleanup, `SELECT pg_advisory_unlock_shared($1)`, temporaryLock); err != nil {
			_ = conn.Conn().Close(cleanup)
		}
	}()
	if err = tx.Commit(ctx); err != nil {
		return entry, err
	}

	dispatch, err := conn.Begin(ctx)
	if err != nil {
		return entry, err
	}
	defer dispatch.Rollback(ctx)
	intent.State, intent.DispatchState, intent.Reason = "rejected", "not_dispatched", "NATIVE_WALLET_PRECONDITION_CHANGED"
	env, active, err = s.weeklyWalletGroupEnvironment(ctx, dispatch, participant, manual)
	var authorityErr error
	if err != nil {
		if !weeklyIssuanceAuthorityLost(err) {
			return entry, err
		}
		authorityErr = err
	}
	if active {
		current, _, _, _, err := s.weeklyIssuanceCandidate(ctx, dispatch, env, pool, manual)
		if err != nil {
			if !weeklyIssuanceAuthorityLost(err) {
				return entry, err
			}
			authorityErr = err
		}
		if err == nil && current.basis == entry.basis {
			kind, secret, err := s.decrypt(env.config)
			if err != nil {
				return entry, err
			}
			err = s.postNativeWalletAdjustment(ctx, dispatch, env.config, kind, secret, grant, intent, func() (bool, error) {
				if manual != nil {
					if _, err := s.auth.AuthorizeAdminSession(ctx, dispatch, manual.token); err != nil {
						if weeklyIssuanceAuthorityLost(err) {
							authorityErr = err
							return false, nil
						}
						return false, err
					}
				}
				now := time.Now()
				for _, part := range entry.Issuance.Contributions {
					if !now.Before(part.End) {
						return false, nil
					}
				}
				return true, nil
			})
			if err != nil {
				return entry, err
			}
		}
	}
	intent.Details.Reasons = []string{intent.Reason}
	if manual == nil {
		actor = ""
	}
	if err = saveWalletApplicationResult(ctx, dispatch, intent, actor, "weekly_wallet"); err != nil {
		return entry, err
	}
	entry.State = intent.State
	if err = dispatch.Commit(ctx); err != nil {
		return entry, err
	}
	return entry, authorityErr
}

func weeklyIssuanceConflict(manual *weeklyWalletManual) error {
	if manual != nil {
		return auth.ErrConflict
	}
	return nil
}

func weeklyIssuanceAuthorityLost(err error) bool {
	return errors.Is(err, auth.ErrUnauthorized) || errors.Is(err, auth.ErrForbidden) || errors.Is(err, auth.ErrPasswordChangeRequired)
}

func (s *Service) recoverWeeklyIssuance(ctx context.Context, tx pgx.Tx, c storedConfig, member Participant, id string) error {
	out, err := scanWalletApplication(tx.QueryRow(ctx, `SELECT `+walletApplicationColumns+` FROM wallet_application_requests r WHERE r.id=$1`, id))
	if err != nil {
		return err
	}
	if out.State != "pending" || out.Details == nil || out.Details.NativeAdjustment == nil ||
		out.Details.Input.SourceID != member.Binding.SourceID || out.Details.UpstreamUserID != member.Binding.UpstreamUserID {
		return nil
	}
	s.observeNativeWalletApplication(ctx, c, &out)
	out.Details.Reasons = []string{out.Reason}
	if err = saveWalletApplicationResult(ctx, tx, &out, "", "weekly_wallet"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
