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
	"sub-pool/backend/internal/allocation"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/estimation"
)

type weeklyWalletGroupIssue struct {
	weeklyWalletAccountKey
	Reason string `json:"reason"`
}

type weeklyWalletGroupEnvironment struct {
	config storedConfig
	member Participant
	policy MonitoringConfig
	pools  []weeklyWalletRef
}

func weeklyWalletGroupRequired(ctx context.Context, tx pgx.Tx, participant string) (bool, error) {
	var grouped bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM weekly_wallet_groups WHERE participant_id=$1) OR (
	 SELECT count(*)>1 FROM pool_contract_participants m JOIN pool_contracts pc ON pc.id=m.contract_id AND pc.enabled
	 JOIN pools p ON p.id=pc.pool_id AND p.revision=pc.revision AND p.enabled
	 LEFT JOIN pool_contract_accounts ca ON ca.contract_id=pc.id WHERE m.participant_id=$1)`, participant).Scan(&grouped)
	return grouped, err
}

func (s *Service) weeklyWalletGroupEnvironment(ctx context.Context, tx pgx.Tx, participant string, manual *weeklyWalletManual) (weeklyWalletGroupEnvironment, bool, error) {
	var env weeklyWalletGroupEnvironment
	var err error
	if manual != nil {
		if _, err = s.auth.AuthorizeAdminSession(ctx, tx, manual.token); err != nil {
			return env, false, err
		}
	}
	env.policy, err = readMonitoringConfig(ctx, tx, false)
	if err != nil || !env.policy.Enabled {
		return env, false, err
	}
	env.config, err = s.config(ctx, tx, false)
	if err != nil || env.config.SourceID == nil || !env.config.CredentialSet || env.config.reverificationRequired {
		return env, false, err
	}
	env.member, err = readParticipant(ctx, tx, participant, env.config, false, true)
	if errors.Is(err, auth.ErrNotFound) {
		return env, false, nil
	}
	if err != nil || !env.member.Enabled || !env.member.SourceCurrent || env.member.Binding.EndedAt != nil {
		return env, false, err
	}
	rows, err := tx.Query(ctx, `SELECT p.id,p.name FROM pools p
	 JOIN pool_contracts pc ON pc.pool_id=p.id AND pc.revision=p.revision AND pc.enabled
	 JOIN pool_contract_participants m ON m.contract_id=pc.id
	 WHERE p.enabled AND m.participant_id=$1 AND m.binding_id=$2 ORDER BY p.id`, participant, env.member.Binding.ID)
	if err != nil {
		return env, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var ref weeklyWalletRef
		if err = rows.Scan(&ref.ID, &ref.Name); err != nil {
			return env, false, err
		}
		env.pools = append(env.pools, ref)
	}
	return env, len(env.pools) != 0, rows.Err()
}

type weeklyWalletGroupOrigin struct {
	id     string
	plan   weeklyWalletGroupPlan
	legacy bool
}

func readWeeklyWalletGroupOrigin(ctx context.Context, tx pgx.Tx, member Participant) (weeklyWalletGroupOrigin, error) {
	var prior weeklyWalletGroupOrigin
	err := tx.QueryRow(ctx, `SELECT 'group:'||g.id::text,g.snapshot FROM weekly_wallet_groups g
	 LEFT JOIN wallet_application_requests r ON r.id=g.application_id
	 WHERE g.binding_id=$1 AND g.participant_id=$2 AND
	  (g.application_id IS NULL OR `+walletApplicationSnapshotSQL+`->>'state'='applied')
	 ORDER BY g.id DESC LIMIT 1`, member.Binding.ID, member.ID).Scan(&prior.id, &prior.plan)
	if !errors.Is(err, pgx.ErrNoRows) {
		return prior, err
	}
	prior.plan.Target, prior.plan.Cutoff = "0", member.Binding.StartedAt
	var oldPool, oldAccount, phase string
	var end time.Time
	var old weeklyWalletPlan
	err = tx.QueryRow(ctx, `SELECT 'legacy:'||w.id::text,w.pool_id,w.account_id,w.phase,w.period_end,w.recorded_at,w.snapshot
	 FROM weekly_wallet_allocations w LEFT JOIN wallet_application_requests r ON r.id=w.application_id
	 WHERE w.binding_id=$1 AND w.participant_id=$2 AND
	  (w.application_id IS NULL OR `+walletApplicationSnapshotSQL+`->>'state'='applied')
	 ORDER BY w.id DESC LIMIT 1`, member.Binding.ID, member.ID).Scan(
		&prior.id, &oldPool, &oldAccount, &phase, &end, &prior.plan.Cutoff, &old)
	if errors.Is(err, pgx.ErrNoRows) {
		return prior, nil
	}
	if err != nil {
		return prior, err
	}
	cycle := weeklyWalletCycle{weeklyWalletAccountKey{oldPool, oldAccount}, end.Add(-7 * 24 * time.Hour).UTC(), end.UTC()}
	contribution := weeklyWalletContribution{
		weeklyWalletCycle: cycle, ContractID: old.ContractID, Phase: phase,
		EstimateID: old.EstimateID, SharePercent: old.SharePercent, Gross: old.Gross,
		Spent: old.Spent, OpeningDebt: old.OpeningDebt, Covered: "0", UsedToCover: "0",
		Debt: old.Debt, Remaining: old.Target, LastFormalGross: "0",
	}
	final, err := previousWeeklyWallet(ctx, tx, weeklyWalletPool{ID: oldPool, AccountID: oldAccount}, member, true)
	if final != nil {
		contribution.LastFormalGross = final.Plan.Gross
	}
	prior.legacy, prior.plan.Target = true, old.Target
	prior.plan.Contributions = []weeklyWalletContribution{contribution}
	return prior, err
}

func (s *Service) weeklyWalletGroupContribution(ctx context.Context, tx pgx.Tx, env weeklyWalletGroupEnvironment, p weeklyWalletPool, a storedAccount,
	old *weeklyWalletContribution, manual bool, now, cutoff time.Time) (weeklyWalletContribution, bool, string, error) {
	key := weeklyWalletAccountKey{p.ID, a.ID}
	out := weeklyWalletContribution{
		weeklyWalletCycle: weeklyWalletCycle{weeklyWalletAccountKey: key}, Name: a.Name, ContractID: p.ContractID,
		Phase: "unfunded", Gross: "0", SharePercent: "0", LastFormalGross: "0",
	}
	view, plan, err := readNativeWeeklyPlan(ctx, tx, a, env.config, env.policy, now)
	if err != nil {
		return out, false, "", err
	}
	if !view.activeCycle(now) {
		return out, false, "waiting_estimate", nil
	}
	out.Start, out.End = view.Period.StartAt.UTC().Truncate(time.Microsecond), view.Period.EndAt.UTC().Truncate(time.Microsecond)
	if old != nil {
		out.LastFormalGross = old.LastFormalGross
		drift := out.End.Sub(old.End)
		if drift >= -nativeResetDrift && drift <= nativeResetDrift && view.Observation.At.Before(old.End) {
			out = *old
			out.Name = a.Name
		} else {
			if drift <= nativeResetDrift || view.Observation.At.Before(old.End) || out.Start.Before(old.End.Add(-nativeResetDrift)) {
				return out, false, "waiting_estimate", nil
			}
			out.Start = maxTime(out.Start, old.End)
		}
	}
	if cutoff.Before(out.Start) || !now.Before(out.End) {
		return out, false, "waiting_estimate", nil
	}
	if out.Phase == "settlement" || !manual && p.actorID == "" {
		return out, false, "", nil
	}
	shares, unassigned, err := readWeeklyWalletShares(ctx, tx, p.ContractID)
	if err != nil {
		return out, false, "", err
	}
	units, found := shares[env.member.ID]
	if !found || unassigned < 0 {
		return out, false, "contract_changed", nil
	}
	if (manual || p.Control.AutoAllocate) && len(plan.Points) != 0 {
		latest, err := readNativeWeeklyResult(ctx, tx, a, env.config, plan, false, true)
		if err != nil {
			return out, false, "", err
		}
		if latest != nil {
			capacity, _ := latest.Work.WalletCapacity.Rational()
			current := make(map[string]string, len(shares))
			for id, weight := range shares {
				current[id] = usageDecimal(big.NewRat(weight, shareTotal))
			}
			evaluated := allocation.Evaluate(allocation.Input{
				Remaining: estimation.Exact(capacity), Current: current,
				Complete: true, CostBasis: "wallet_deducted_usd",
			})
			if evaluated.State != "scenario" {
				return out, false, "contract_changed", nil
			}
			gross, _ := evaluated.Members[env.member.ID].Rational()
			out.Gross = usageDecimal(weeklyWalletFloor(gross))
			out.LastFormalGross, out.SharePercent = out.Gross, sharePercent(units)
			out.UnallocatedPercent = sharePercent(unassigned)
			out.ContractID, out.Phase, out.EstimateID = p.ContractID, "settlement", &latest.Result.ID
			return out, true, "", nil
		}
	}
	if view.State == "source_stale" {
		return out, false, "waiting_estimate", nil
	}
	if !manual && p.Control.Enabled && out.Phase == "unfunded" && old != nil {
		gross := weeklyWalletAmount(out.LastFormalGross)
		if gross != nil && gross.Sign() > 0 {
			out.Gross = usageDecimal(weeklyWalletFloor(gross.Quo(gross, big.NewRat(2, 1))))
			out.ContractID, out.Phase, out.SharePercent = p.ContractID, "advance", sharePercent(units)
			out.UnallocatedPercent = sharePercent(unassigned)
			return out, true, "", nil
		}
	}
	if out.Phase == "unfunded" && (manual || p.Control.AutoAllocate) {
		return out, false, "waiting_estimate", nil
	}
	return out, false, "", nil
}

func readWeeklyWalletShares(ctx context.Context, tx pgx.Tx, contract string) (map[string]int64, int64, error) {
	rows, err := tx.Query(ctx, `SELECT participant_id,share_units FROM pool_contract_participants WHERE contract_id=$1`, contract)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	shares := make(map[string]int64)
	unassigned := int64(shareTotal)
	for rows.Next() {
		var id string
		var units int64
		if err = rows.Scan(&id, &units); err != nil {
			return nil, 0, err
		}
		shares[id] = units
		unassigned -= units
	}
	return shares, unassigned, rows.Err()
}

func (s *Service) weeklyWalletGroupCandidate(ctx context.Context, tx pgx.Tx, env weeklyWalletGroupEnvironment, anchor string,
	manual *weeklyWalletManual, cutoff time.Time, sampledBalance *string, usage *weeklyWalletUsagePlan) (weeklyWalletEntry, weeklyWalletPool, []weeklyWalletGroupIssue, string, error) {
	now := time.Now().UTC()
	entry := weeklyWalletEntry{State: "unavailable", ParticipantID: env.member.ID, Name: env.member.Name}
	var driver weeklyWalletPool
	var issues []weeklyWalletGroupIssue
	if usage != nil && !usage.discover() && usage.Binding != env.member.Binding.ID {
		return entry, driver, []weeklyWalletGroupIssue{{weeklyWalletAccountKey{PoolID: anchor}, "contract_changed"}}, "", nil
	}
	if cutoff.After(now) || now.Sub(cutoff) > time.Duration(env.policy.StaleAfterSeconds)*time.Second {
		return entry, driver, issues, "", nil
	}
	c := env.config
	prior, err := readWeeklyWalletGroupOrigin(ctx, tx, env.member)
	if err != nil || prior.plan.Cutoff.After(cutoff) {
		return entry, driver, issues, "", err
	}

	kind, secret, err := s.decrypt(c)
	if err != nil {
		return entry, driver, issues, "", err
	}
	var actor auth.Identity
	if manual != nil {
		actor, err = s.auth.AuthorizeAdminSession(ctx, tx, manual.token)
		if err != nil {
			return entry, driver, issues, "", err
		}
	}
	old := make(map[weeklyWalletAccountKey]*weeklyWalletContribution)
	for i := range prior.plan.Contributions {
		item := &prior.plan.Contributions[i]
		old[item.weeklyWalletAccountKey] = item
	}
	var current []weeklyWalletContribution
	var activity []weeklyWalletUse
	var evidence []any
	seen := make(map[string]weeklyWalletUse)
	used := new(big.Rat)
	var stockPrecision *bool
	collect := func(key weeklyWalletAccountKey, account storedAccount, previous *weeklyWalletContribution, start, end time.Time) ([]string, error) {
		readFrom := maxTime(prior.plan.Cutoff, start)
		if prior.legacy {
			readFrom = start
		}
		request := weeklyWalletUsageRequest{
			Key: weeklyWalletUsageKey{key, usageDayRange{readFrom.UTC(), end.UTC()}}, Account: account,
		}
		if usage.discover() {
			*usage.Requests = append(*usage.Requests, request)
			return nil, nil
		}
		result, err := usage.read(ctx, tx, c, request, env.member.Binding.UpstreamUserID)
		if err != nil {
			return nil, err
		}
		if !result.Ready {
			issues = append(issues, weeklyWalletGroupIssue{key, "usage_unavailable"})
			return nil, nil
		}
		beforeOrigin := new(big.Rat)
		for _, use := range result.Uses {
			if previousUse, duplicate := seen[use.ID]; duplicate {
				if previousUse != use {
					issues = append(issues, weeklyWalletGroupIssue{key, "usage_conflict"})
				}
				continue
			}
			seen[use.ID] = use
			cost := weeklyWalletAmount(use.Cost)
			if cost.Cmp(weeklyWalletFloor(cost)) != 0 {
				if stockPrecision == nil {
					result := s.weeklyWalletStockPrecision(ctx, c, kind, secret)
					stockPrecision = &result
				}
				debit, proven := stockWeeklyWalletDebit(cost)
				if !*stockPrecision || !proven {
					issues = append(issues, weeklyWalletGroupIssue{key, "amount_precision_unverified"})
					return nil, nil
				}
				cost, use.Cost = debit, usageDecimal(debit)
			}
			if use.At.Before(prior.plan.Cutoff) {
				beforeOrigin.Add(beforeOrigin, cost)
				continue
			}
			used.Add(used, cost)
			activity = append(activity, use)
		}
		if prior.legacy {
			spent := "0"
			if previous != nil {
				spent = previous.Spent
			}
			if usageDecimal(beforeOrigin) != spent {
				issues = append(issues, weeklyWalletGroupIssue{key, "balance_mismatch"})
			}
		}
		return result.references(), nil
	}
	entry.Phase = "advance"
	due := false
	for _, ref := range env.pools {
		p, err := s.readWeeklyWalletPool(ctx, tx, c, ref.ID)
		if err != nil {
			return entry, driver, issues, "", err
		}
		var memberCurrent bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pool_contract_participants
		 WHERE contract_id=$1 AND participant_id=$2 AND binding_id=$3)`, p.ContractID, env.member.ID, env.member.Binding.ID).Scan(&memberCurrent)
		if err != nil {
			return entry, driver, issues, "", err
		}
		if manual != nil {
			p.actorID = actor.ID
		} else if p.ID == anchor && p.current(c) {
			identity, authErr := s.auth.AuthorizeAutomationAdmin(ctx, tx, p.Control.ActorID, p.Control.ActorRevision)
			if authErr != nil && !errors.Is(authErr, auth.ErrUnauthorized) && !errors.Is(authErr, auth.ErrForbidden) {
				return entry, driver, issues, "", authErr
			}
			p.actorID = identity.ID
		}
		if ref.ID == anchor {
			driver = p
			if p.actorID == "" && sampledBalance == nil && !usage.discover() {
				return entry, driver, nil, "", nil
			}
		}
		for _, id := range p.AccountIDs {
			key := weeklyWalletAccountKey{p.ID, id}
			if !memberCurrent || !slices.Contains(p.ReadyAccounts, id) {
				issues = append(issues, weeklyWalletGroupIssue{key, "account_unavailable"})
				continue
			}
			account, err := readManaged(ctx, tx, id, c, false)
			if err != nil {
				return entry, driver, issues, "", err
			}
			previous := old[key]
			delete(old, key)
			if previous != nil && previous.Name == "" {
				previous.Name = account.Name
			}
			next, changed, reason, err := s.weeklyWalletGroupContribution(ctx, tx, env, p, account, previous, manual != nil || sampledBalance != nil || usage.discover(), now, cutoff)
			if err != nil {
				return entry, driver, issues, "", err
			}
			if reason != "" {
				issues = append(issues, weeklyWalletGroupIssue{key, reason})
				continue
			}
			if changed {
				due = true
				if next.Phase == "settlement" {
					entry.Phase = "settlement"
				}
			}
			current = append(current, next)
			start := maxTime(next.Start, env.member.Binding.StartedAt)
			if previous != nil {
				start = maxTime(previous.Start, env.member.Binding.StartedAt)
			}
			refs, err := collect(key, account, previous, start, cutoff)
			if err != nil {
				return entry, driver, issues, "", err
			}
			evidence = append(evidence, []any{key, p.Revision, p.ContractID, p.Control, p.actorID, account.Revision, refs})
		}
		if len(p.AccountIDs) == 0 {
			issues = append(issues, weeklyWalletGroupIssue{weeklyWalletAccountKey{PoolID: p.ID}, "account_unavailable"})
		}
	}
	for i := range prior.plan.Contributions {
		previous := &prior.plan.Contributions[i]
		key := previous.weeklyWalletAccountKey
		if old[key] == nil {
			continue
		}
		var verified bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pool_contract_accounts ca JOIN managed_accounts a ON a.id=ca.account_id
		 WHERE ca.contract_id=$1 AND ca.account_id=$2 AND a.source_id=$3 AND ca.source_id=a.source_id
		  AND a.resource_fingerprint=ca.resource_fingerprint AND a.identity_state='verified')`, previous.ContractID, key.AccountID, c.SourceID).Scan(&verified)
		if err != nil {
			return entry, driver, issues, "", err
		}
		if !verified || slices.ContainsFunc(current, func(next weeklyWalletContribution) bool {
			return next.AccountID == key.AccountID && next.Start.Before(previous.End)
		}) {
			issues = append(issues, weeklyWalletGroupIssue{key, "contract_changed"})
			continue
		}
		account, err := readManaged(ctx, tx, key.AccountID, c, false)
		if err != nil {
			return entry, driver, issues, "", err
		}
		if previous.Name == "" {
			previous.Name = account.Name
		}
		end := cutoff
		if previous.End.After(cutoff) {
			current = append(current, *previous)
		} else {
			end = previous.End
		}
		refs, err := collect(key, account, previous, maxTime(previous.Start, env.member.Binding.StartedAt), end)
		if err != nil {
			return entry, driver, issues, "", err
		}
		evidence = append(evidence, []any{key, previous.ContractID, account.Revision, refs})
	}
	if usage.discover() || len(issues) != 0 {
		entry.State = "waiting_evidence"
		return entry, driver, issues, "", nil
	}
	if driver.ID == "" || driver.actorID == "" && sampledBalance == nil {
		return entry, driver, nil, "", nil
	}
	if !due {
		entry.State = "settled"
		if manual != nil && slices.ContainsFunc(current, func(source weeklyWalletContribution) bool {
			return source.Phase == "advance"
		}) {
			entry.State = "waiting_estimate"
		}
		return entry, driver, nil, "", nil
	}
	var grantID string
	preview := manual != nil && manual.preview
	balance := sampledBalance
	if balance == nil {
		var reason string
		grantID, reason, err = s.writeGrant(ctx, tx, c, writeWallet, false)
		if err != nil || reason != "" && !preview {
			entry.State = "write_unavailable"
			return entry, driver, nil, "", err
		}
		var frozen *string
		balance, frozen = s.nativeWalletFields(ctx, c, kind, secret, env.member.Binding.UpstreamUserID)
		if frozen == nil || *frozen != "0" {
			balance = nil
		}
	}
	expected := weeklyWalletAmount(prior.plan.Target)
	if expected == nil || balance == nil {
		entry.State = "balance_unavailable"
		return entry, driver, nil, "", nil
	}
	if usageDecimal(expected.Sub(expected, used)) != *balance {
		entry.State = "waiting_evidence"
		return entry, driver, []weeklyWalletGroupIssue{{weeklyWalletAccountKey{PoolID: anchor}, "balance_mismatch"}}, "", nil
	}
	plan, ok := calculateWeeklyWalletGroup(prior.plan.Contributions, current, activity, cutoff, *balance)
	if !ok {
		entry.State = "waiting_evidence"
		key := weeklyWalletAccountKey{PoolID: anchor}
		if len(plan.Closed) > 0 {
			key = plan.Closed[len(plan.Closed)-1].weeklyWalletAccountKey
		}
		return entry, driver, []weeklyWalletGroupIssue{{key, "contract_changed"}}, "", nil
	}
	if grantID != "" && ((plan.Delta != "0" && !validNativeDelta(plan.Delta)) || !s.weeklyWalletWriteAvailable(ctx, c, kind, secret, plan.Delta[0] != '-')) {
		grantID = ""
	}
	if sampledBalance == nil && grantID == "" && !preview {
		entry.State = "write_unavailable"
		return entry, driver, nil, "", nil
	}
	entry.Group, entry.PeriodEnd, entry.RecordedAt = &plan, plan.Contributions[0].End, cutoff
	entry.State = "ready"
	if grantID == "" {
		return entry, driver, nil, "", nil
	}
	body, err := json.Marshal([]any{anchor, c.SourceID, c.Revision, env.member.Revision, env.member.Binding.ID, prior.id, grantID, evidence, activity, plan})
	if err != nil {
		return entry, driver, nil, "", err
	}
	entry.basis = fmt.Sprintf("%x", sha256.Sum256(body))
	return entry, driver, nil, grantID, nil
}
