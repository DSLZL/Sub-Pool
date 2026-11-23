package connection

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
)

func (s *Service) weeklyWalletGroupRecommendation(ctx context.Context, tx pgx.Tx, token string, out WeeklyWalletRecommendation) (WeeklyWalletRecommendation, error) {
	manual := &weeklyWalletManual{token: token}
	env, ok, err := s.weeklyWalletGroupEnvironment(ctx, tx, out.ParticipantID, manual)
	if err != nil {
		return out, err
	}
	if ok {
		out.Pool = &env.pools[0]
		pending, err := weeklyWalletPending(ctx, tx, out.ParticipantID)
		if err != nil {
			return out, err
		}
		out.State = "pending"
		if !pending {
			cutoff := time.Now().UTC().Truncate(time.Microsecond)
			entry, _, issues, _, err := s.weeklyWalletGroupCandidate(ctx, tx, env, out.Pool.ID, manual, cutoff, nil)
			if err != nil {
				return out, err
			}
			out.State, out.Issues = entry.State, issues
			if entry.Group != nil {
				out.Proposal = &weeklyWalletProposal{Basis: entry.basis, PeriodEnd: entry.PeriodEnd, Delta: entry.Group.Delta, Group: entry.Group}
			}
		}
	}
	if _, err = s.auth.AuthorizeAdminSession(ctx, tx, token); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func saveWeeklyWalletGroup(ctx context.Context, tx pgx.Tx, binding string, entry *weeklyWalletEntry) error {
	pools := make([]string, 0, len(entry.Group.Contributions))
	for _, source := range entry.Group.Contributions {
		pools = append(pools, source.PoolID)
	}
	for _, source := range entry.Group.Closed {
		pools = append(pools, source.PoolID)
	}
	slices.Sort(pools)
	return tx.QueryRow(ctx, `INSERT INTO weekly_wallet_groups
	 (participant_id,binding_id,pool_ids,phase,period_end,application_id,recorded_at,basis,snapshot)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id::text`, entry.ParticipantID, binding, slices.Compact(pools),
		entry.Phase, entry.PeriodEnd, entry.ApplicationID, entry.RecordedAt, entry.basis, entry.Group).Scan(&entry.ID)
}

func listWeeklyWalletGroups(ctx context.Context, tx pgx.Tx, pool string) ([]weeklyWalletEntry, error) {
	rows, err := tx.Query(ctx, `SELECT g.id::text,g.participant_id,p.name,g.phase,g.period_end,g.recorded_at,g.snapshot,
	 g.application_id::text,`+walletApplicationSnapshotSQL+`->>'state' FROM weekly_wallet_groups g
	 JOIN participants p ON p.id=g.participant_id LEFT JOIN wallet_application_requests r ON r.id=g.application_id
	 WHERE g.pool_ids @> ARRAY[$1]::text[] ORDER BY g.id DESC LIMIT 100`, pool)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []weeklyWalletEntry
	for rows.Next() {
		var entry weeklyWalletEntry
		var state *string
		if err = rows.Scan(&entry.ID, &entry.ParticipantID, &entry.Name, &entry.Phase, &entry.PeriodEnd, &entry.RecordedAt, &entry.Group, &entry.ApplicationID, &state); err != nil {
			return nil, err
		}
		entry.PeriodEnd, entry.RecordedAt = entry.PeriodEnd.UTC(), entry.RecordedAt.UTC()
		entry.State = "applied"
		if state != nil {
			entry.State = *state
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

func (s *Service) executeWeeklyWalletGroup(ctx context.Context, tx pgx.Tx, expected weeklyWalletPool, entry weeklyWalletEntry,
	out WalletApplication, manual *weeklyWalletManual, grantID string) (string, error) {
	env, ok, authorityErr := s.weeklyWalletGroupEnvironment(ctx, tx, out.ParticipantID, manual)
	if authorityErr != nil && !errors.Is(authorityErr, auth.ErrUnauthorized) && !errors.Is(authorityErr, auth.ErrForbidden) {
		return "", authorityErr
	}
	out.State, out.DispatchState, out.Reason = "rejected", "not_dispatched", "NATIVE_WALLET_PRECONDITION_CHANGED"
	if ok {
		current, driver, _, currentGrant, err := s.weeklyWalletGroupCandidate(ctx, tx, env, expected.ID, manual, entry.Group.Cutoff, nil)
		if err != nil {
			return "", err
		}
		if current.State == "ready" && current.basis == entry.basis && currentGrant == grantID && driver.actorID == expected.actorID {
			kind, secret, err := s.decrypt(env.config)
			if err != nil {
				return "", err
			}
			err = s.postNativeWalletAdjustment(ctx, tx, env.config, kind, secret, grantID, &out, func() (bool, error) {
				live, eligible, err := s.weeklyWalletGroupEnvironment(ctx, tx, out.ParticipantID, manual)
				if errors.Is(err, auth.ErrUnauthorized) || errors.Is(err, auth.ErrForbidden) {
					authorityErr = err
					return false, nil
				}
				if err != nil || !eligible || !slices.Equal(live.pools, env.pools) ||
					live.config.Revision != env.config.Revision || live.member.Revision != env.member.Revision ||
					!time.Now().Before(entry.PeriodEnd) {
					return false, err
				}
				if manual == nil {
					if _, err = s.auth.AuthorizeAutomationAdmin(ctx, tx, expected.Control.ActorID, expected.Control.ActorRevision); err != nil {
						if errors.Is(err, auth.ErrUnauthorized) || errors.Is(err, auth.ErrForbidden) {
							return false, nil
						}
						return false, err
					}
				}
				balance, frozen := s.nativeWalletFields(ctx, env.config, kind, secret, env.member.Binding.UpstreamUserID)
				return balance != nil && *balance == entry.Group.BalanceBefore && frozen != nil && *frozen == "0", nil
			})
			if err != nil {
				return "", err
			}
		}
	}
	out.Details.Reasons = []string{out.Reason}
	actor := ""
	if manual != nil {
		actor = expected.actorID
	}
	if err := saveWalletApplicationResult(ctx, tx, &out, actor, "weekly_wallet"); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return out.State, authorityErr
}
