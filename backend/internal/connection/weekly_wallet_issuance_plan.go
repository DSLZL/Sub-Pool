package connection

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
)

func (s *Service) weeklyIssuanceCandidate(ctx context.Context, tx pgx.Tx, env weeklyWalletGroupEnvironment, anchor string, manual *weeklyWalletManual) (weeklyWalletEntry, []weeklyIssuanceIssue, string, string, error) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	entry := weeklyWalletEntry{ParticipantID: env.member.ID, Name: env.member.Name, RecordedAt: now, State: "unavailable"}
	var issues []weeklyIssuanceIssue
	var actorID string
	var identity auth.Identity
	if manual != nil {
		var err error
		identity, err = s.auth.AuthorizeAdminSession(ctx, tx, manual.token)
		if err != nil {
			return entry, nil, "", "", err
		}
	}
	book, err := readWeeklyIssuanceBook(ctx, tx, env.member)
	if err != nil {
		return entry, nil, "", "", err
	}
	var authority []weeklyIssuanceAuthority
	plan := &weeklyIssuancePlan{}
	anchorFound := false
	for _, ref := range env.pools {
		p, err := s.readWeeklyWalletPool(ctx, tx, env.config, ref.ID)
		if err != nil {
			return entry, nil, "", "", err
		}
		actor := identity
		if manual == nil {
			if !p.current(env.config) {
				continue
			}
			actor, err = s.auth.AuthorizeAutomationAdmin(ctx, tx, p.Control.ActorID, p.Control.ActorRevision)
			if errors.Is(err, auth.ErrUnauthorized) || errors.Is(err, auth.ErrForbidden) || errors.Is(err, auth.ErrPasswordChangeRequired) {
				continue
			}
			if err != nil {
				return entry, nil, "", "", err
			}
		}
		var units, total int64
		err = tx.QueryRow(ctx, `SELECT share_units,
		 (SELECT sum(share_units) FROM pool_contract_participants WHERE contract_id=$1)
		 FROM pool_contract_participants WHERE contract_id=$1 AND participant_id=$2 AND binding_id=$3`,
			p.ContractID, env.member.ID, env.member.Binding.ID).Scan(&units, &total)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return entry, nil, "", "", err
		}
		if p.ID == anchor {
			anchorFound, actorID = true, actor.ID
		}
		for _, id := range p.AccountIDs {
			key := weeklyWalletAccountKey{p.ID, id}
			if len(plan.Contributions) >= 500 {
				break
			}
			if total > shareTotal {
				issues = append(issues, weeklyIssuanceIssue{weeklyWalletGroupIssue: weeklyWalletGroupIssue{key, "contract_changed"}})
				continue
			}
			if !slices.Contains(p.ReadyAccounts, id) {
				issues = append(issues, weeklyIssuanceIssue{weeklyWalletGroupIssue: weeklyWalletGroupIssue{key, "account_unavailable"}})
				continue
			}
			account, err := readManaged(ctx, tx, id, env.config, false)
			if err != nil {
				return entry, nil, "", "", err
			}
			item, reason, err := weeklyIssuanceDue(ctx, tx, env, p, account, units, book, manual != nil, now)
			if err != nil {
				return entry, nil, "", "", err
			}
			if reason != "" {
				issue := weeklyIssuanceIssue{weeklyWalletGroupIssue: weeklyWalletGroupIssue{key, reason}}
				if item != nil {
					issue.Usage = item.Usage
				}
				issues = append(issues, issue)
			}
			if item != nil && item.Phase != "" {
				plan.Contributions = append(plan.Contributions, *item)
				authority = append(authority, weeklyIssuanceAuthority{
					PoolID: p.ID, PoolRevision: p.Revision, ContractID: p.ContractID, ControlRevision: p.Control.Revision,
					AccountID: id, AccountRevision: account.Revision, ActorID: actor.ID, ActorRevision: actor.Revision,
				})
			}
		}
	}
	if !anchorFound {
		return entry, issues, "", "", nil
	}
	issues = append(issues, book.apply(plan)...)
	if len(issues) > 500 {
		issues = issues[:500]
	}
	entry.State = "not_due"
	if len(plan.Contributions) == 0 {
		if len(issues) != 0 {
			entry.State = "waiting_estimate"
			for _, issue := range issues {
				switch issue.Reason {
				case "legacy_credit_unverified":
					entry.State = "legacy_credit_unverified"
				case "amount_precision_unverified":
					if entry.State != "legacy_credit_unverified" {
						entry.State = "amount_precision_unverified"
					}
				case "waiting_actual_cost":
					if entry.State == "waiting_estimate" {
						entry.State = "waiting_actual_cost"
					}
				}
			}
		}
		return entry, issues, "", "", nil
	}
	entry.State, entry.Issuance = "ready", plan
	summarizeWeeklyIssuance(&entry)
	var grantID string
	if plan.Amount != "0" {
		id, reason, err := s.writeGrant(ctx, tx, env.config, writeWallet, false)
		if err != nil {
			return entry, issues, "", "", err
		}
		if reason != "" {
			return entry, issues, "", "", nil
		}
		grantID = id
	}
	basis, err := json.Marshal([]any{
		env.member.ID, env.member.Revision, env.member.Binding.ID, env.member.Binding.UpstreamUserID,
		env.config.SourceID, env.config.Revision, env.policy.Revision, anchor, authority, grantID, plan.Contributions,
		plan.OutstandingDebt,
	})
	if err != nil {
		return entry, issues, "", "", err
	}
	entry.basis = fmt.Sprintf("%x", sha256.Sum256(basis))
	entry.issuanceRecord = weeklyIssuanceRecord{Authority: authority, WalletGrant: grantID, PolicyRevision: env.policy.Revision}
	return entry, issues, actorID, grantID, nil
}

func weeklyIssuanceDue(ctx context.Context, tx pgx.Tx, env weeklyWalletGroupEnvironment, pool weeklyWalletPool, account storedAccount, units int64,
	book weeklyIssuanceBook, manual bool, now time.Time) (*weeklyIssuanceContribution, string, error) {
	view, plan, err := readNativeWeeklyPlan(ctx, tx, account, env.config, env.policy, now)
	if err != nil {
		return nil, "", err
	}
	if !view.activeCycle(now) {
		return nil, "waiting_estimate", nil
	}
	key := weeklyWalletAccountKey{pool.ID, account.ID}
	cycle := weeklyWalletCycle{key, view.Period.StartAt.UTC().Truncate(time.Microsecond), view.Period.EndAt.UTC().Truncate(time.Microsecond)}
	var previous, lastFormal *weeklyIssuancePosition
	for i := range book {
		item := &book[i]
		if item.weeklyWalletAccountKey != key {
			continue
		}
		if previous == nil || item.End.After(previous.End) {
			previous = item
		}
		if !item.retry && item.Phase == "settlement" && !item.End.After(cycle.Start.Add(nativeResetDrift)) && (lastFormal == nil || item.End.After(lastFormal.End)) {
			lastFormal = item
		}
	}
	if previous != nil {
		drift := cycle.End.Sub(previous.End)
		switch {
		case drift >= -nativeResetDrift && drift <= nativeResetDrift && view.Observation.At.Before(previous.End):
			cycle = previous.weeklyWalletCycle
		case drift <= nativeResetDrift || view.Observation.At.Before(previous.End) || cycle.Start.Before(previous.End.Add(-nativeResetDrift)):
			return nil, "waiting_estimate", nil
		default:
			cycle.Start = maxTime(cycle.Start, previous.End)
		}
	}
	if now.Before(cycle.Start) || !now.Before(cycle.End) {
		return nil, "waiting_estimate", nil
	}
	advanced := false
	for _, item := range book {
		if item.retry || item.weeklyWalletAccountKey != key || !item.End.Equal(cycle.End) {
			continue
		}
		if item.Phase == "settlement" {
			return nil, "", nil
		}
		if item.ambiguous {
			return nil, "legacy_credit_unverified", nil
		}
		advanced = true
	}
	out := &weeklyIssuanceContribution{
		weeklyWalletCycle: cycle, Name: account.Name, ContractID: pool.ContractID, Share: sharePercent(units),
	}
	var capacity *big.Rat
	if manual || pool.Control.AutoAllocate {
		latest, err := readNativeWeeklyResult(ctx, tx, account, env.config, plan, false, true)
		if err != nil {
			return nil, "", err
		}
		if latest != nil {
			capacity, _ = latest.Work.WalletCapacity.Rational()
			out.Phase, out.EstimateID = "settlement", &latest.Result.ID
			out.Usage = weeklyIssuanceSampleUsage(latest)
		}
	}
	if capacity != nil {
		out.Gross = usageDecimal(capacity.Mul(capacity, big.NewRat(units, shareTotal)))
		return out, "", nil
	}
	if !manual && view.State != "source_stale" && pool.Control.Enabled && !advanced {
		if lastFormal != nil {
			if gross := weeklyWalletAmount(lastFormal.Gross); gross != nil && gross.Sign() > 0 {
				out.Phase, out.Gross = "advance", usageDecimal(gross.Quo(gross, big.NewRat(2, 1)))
				return out, "", nil
			}
		}
		if configured := pool.Control.FirstWeekCapacity[account.ID]; configured != "" {
			eligible, err := weeklyIssuanceFirstCycle(ctx, tx, account, cycle)
			if err != nil {
				return nil, "", err
			}
			if eligible {
				if n := weeklyWalletAmount(configured); n != nil && n.Sign() > 0 {
					out.Phase = "advance"
					out.Gross = usageDecimal(n.Mul(n, big.NewRat(units, 2*shareTotal)))
					return out, "", nil
				}
			}
		}
	}
	if manual || pool.Control.AutoAllocate {
		if len(plan.Points) != 0 {
			reference, err := readNativeWeeklyResult(ctx, tx, account, env.config, plan, true, false)
			if err != nil {
				return nil, "", err
			}
			if reference != nil {
				out.Usage = weeklyIssuanceSampleUsage(reference)
				return out, "waiting_actual_cost", nil
			}
		}
		return nil, "waiting_estimate", nil
	}
	return nil, "", nil
}

func weeklyIssuanceSampleUsage(job *nativeWeeklyJob) *weeklyIssuanceUsage {
	usage := weeklyIssuanceUsage{Observed: job.Result.Observation.At.UTC()}
	diagnosed := 0
	for _, costs := range [][]nativeWeeklyCost{job.Work.Costs, job.Work.Tail} {
		for _, cost := range costs {
			usage.Rows += cost.Records
			usage.Unattributed += cost.UnattributedRows
			usage.CostIssues += cost.CostIssueRows
			diagnosed += cost.DiagnosedRows
		}
	}
	if usage.Rows == 0 || usage.Rows != diagnosed {
		return nil
	}
	return &usage
}

func weeklyIssuanceFirstCycle(ctx context.Context, tx pgx.Tx, account storedAccount, cycle weeklyWalletCycle) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
	 SELECT 1 FROM pools p JOIN pool_contracts c ON c.pool_id=p.id AND c.revision=1
	 JOIN pool_contract_accounts a ON a.contract_id=c.id
	 WHERE p.id=$1 AND p.created_at >= $2 AND p.created_at < $3
	  AND a.account_id=$4 AND a.source_id=$5 AND a.resource_fingerprint=$6)`,
		cycle.PoolID, cycle.Start, cycle.End, account.ID, account.SourceID, account.fingerprint).Scan(&ok)
	return ok, err
}
