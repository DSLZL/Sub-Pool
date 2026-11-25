package connection

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
)

type weeklyWalletPlan struct {
	Gross         string  `json:"gross"`
	Spent         string  `json:"spent"`
	OpeningDebt   string  `json:"opening_debt"`
	Debt          string  `json:"debt"`
	BalanceBefore string  `json:"balance_before"`
	Target        string  `json:"target"`
	ContractID    string  `json:"contract_id"`
	EstimateID    *string `json:"estimate_id"`
	SharePercent  string  `json:"share_percent,omitempty"`
}

type weeklyWalletManual struct {
	token, basis string
	cutoff       *time.Time
	preview      bool
}

func weeklyWalletAmount(value string) *big.Rat {
	v, _ := new(big.Rat).SetString(value)
	return v
}

func weeklyWalletFloor(value *big.Rat) *big.Rat {
	quantum := big.NewInt(100000000)
	ticks := new(big.Int).Mul(value.Num(), quantum)
	ticks.Quo(ticks, value.Denom())
	return new(big.Rat).SetFrac(ticks, quantum)
}

func calculateWeeklyWallet(gross *big.Rat, before string, prior *weeklyWalletEntry, sameCycle bool) (weeklyWalletPlan, bool) {
	balance := weeklyWalletAmount(before)
	if gross == nil || gross.Sign() < 0 || balance == nil || weeklyWalletFloor(balance).Cmp(balance) != 0 {
		return weeklyWalletPlan{}, false
	}
	debt, spent := new(big.Rat), new(big.Rat)
	if prior != nil {
		if sameCycle {
			debt = weeklyWalletAmount(prior.Plan.OpeningDebt)
			previous := weeklyWalletAmount(prior.Plan.Target)
			if previous == nil || previous.Sign() < 0 {
				return weeklyWalletPlan{}, false
			}
			spent.Sub(previous, balance)
			if spent.Sign() < 0 {
				return weeklyWalletPlan{}, false
			}
		} else {
			debt = weeklyWalletAmount(prior.Plan.Debt)
		}
	}
	if debt == nil || debt.Sign() < 0 {
		return weeklyWalletPlan{}, false
	}
	if !sameCycle && balance.Sign() < 0 {
		debt.Sub(debt, balance)
	}
	gross = weeklyWalletFloor(gross)
	net := new(big.Rat).Sub(gross, debt)
	net.Sub(net, spent)
	target, carried := net, new(big.Rat)
	if net.Sign() < 0 {
		target, carried = new(big.Rat), new(big.Rat).Neg(net)
	}
	return weeklyWalletPlan{
		Gross: usageDecimal(gross), Spent: usageDecimal(spent), OpeningDebt: usageDecimal(debt),
		Debt: usageDecimal(carried), BalanceBefore: before, Target: usageDecimal(target),
	}, true
}

func previousWeeklyWallet(ctx context.Context, tx pgx.Tx, p weeklyWalletPool, member Participant, finalized bool) (*weeklyWalletEntry, error) {
	var out weeklyWalletEntry
	err := tx.QueryRow(ctx, `SELECT w.id::text,w.phase,w.period_end,w.snapshot FROM weekly_wallet_allocations w
	 LEFT JOIN wallet_application_requests r ON r.id=w.application_id
	 WHERE w.pool_id=$1 AND w.account_id=$2 AND w.binding_id=$3 AND (NOT $4 OR w.phase='settlement')
	  AND (w.application_id IS NULL OR `+walletApplicationSnapshotSQL+`->>'state'='applied')
	 ORDER BY w.id DESC LIMIT 1`, p.ID, p.AccountID, member.Binding.ID, finalized).Scan(&out.ID, &out.Phase, &out.PeriodEnd, &out.Plan)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &out, err
}

func (s *Service) weeklyWalletDue(ctx context.Context, tx pgx.Tx, p weeklyWalletPool, member Participant, a storedAccount, c storedConfig, policy MonitoringConfig, manual bool) (weeklyWalletEntry, *weeklyWalletEntry, *big.Rat, bool, error) {
	out := weeklyWalletEntry{State: "waiting_estimate"}
	now := time.Now().UTC()
	view, plan, err := readNativeWeeklyPlan(ctx, tx, a, c, policy, now)
	if err != nil || view == nil || view.Period == nil || view.Observation == nil || view.stale(policy, now) ||
		view.State != "collecting" && view.State != "awaiting_increase" {
		return out, nil, nil, false, err
	}
	prior, err := previousWeeklyWallet(ctx, tx, p, member, false)
	if err != nil {
		return out, nil, nil, false, err
	}
	out.PeriodEnd = view.Period.EndAt.UTC().Truncate(time.Microsecond)
	same := false
	if prior != nil {
		drift := view.Period.EndAt.Sub(prior.PeriodEnd)
		same = drift >= -nativeResetDrift && drift <= nativeResetDrift && view.Observation.At.Before(prior.PeriodEnd)
		if same {
			out.PeriodEnd = prior.PeriodEnd.UTC()
			if prior.Phase == "settlement" {
				out.State = "settled"
				return out, prior, nil, same, nil
			}
		} else if drift <= nativeResetDrift || view.Observation.At.Before(prior.PeriodEnd) || view.Period.StartAt.Before(prior.PeriodEnd.Add(-nativeResetDrift)) {
			return out, prior, nil, false, nil
		}
	}
	if (manual || p.Control.AutoAllocate) && len(plan.Points) != 0 {
		_, latest, err := readNativeWeeklyWork(ctx, tx, a.ID, plan.key())
		if err != nil {
			return out, nil, nil, false, err
		}
		if latest != nil && latest.Result.Cumulative != nil && latest.Result.Cumulative.Confidence == "trusted" &&
			plan.includesCompleted(latest.Work.Plan) && view.Observation.UsedPercent == latest.Result.Observation.UsedPercent {
			capacity, valid := latest.Work.WalletCapacity.Rational()
			if latest.Work.Plan.CostVersion == plan.CostVersion && valid && capacity.Sign() > 0 {
				var units int64
				err = tx.QueryRow(ctx, `SELECT share_units FROM pool_contract_participants
				 WHERE contract_id=$1 AND participant_id=$2 AND binding_id=$3`, p.ContractID, member.ID, member.Binding.ID).Scan(&units)
				if err != nil {
					return out, nil, nil, false, err
				}
				out.Phase, out.Plan.EstimateID = "settlement", &latest.Result.ID
				out.Plan.SharePercent = sharePercent(units)
				return out, prior, capacity.Mul(capacity, big.NewRat(units, shareTotal)), same, nil
			}
			out.State = "waiting_actual_cost"
		}
	}
	if manual || !p.Control.Enabled || prior == nil || same {
		return out, prior, nil, same, nil
	}
	final, err := previousWeeklyWallet(ctx, tx, p, member, true)
	if err != nil || final == nil {
		return out, prior, nil, false, err
	}
	out.Phase = "advance"
	gross := weeklyWalletAmount(final.Plan.Gross)
	if gross != nil && gross.Sign() > 0 {
		gross.Quo(gross, big.NewRat(2, 1))
	}
	return out, prior, gross, false, nil
}

// Round robin keeps a blocked wallet from delaying the other members.
func (s *Service) processWeeklyWallet(ctx context.Context, after string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	release, err := s.Runtime.Enter(ctx)
	if err != nil {
		return after, err
	}
	defer release()
	var pool, member string
	afterMember, afterPool, _ := strings.Cut(after, "/")
	err = s.pool.QueryRow(ctx, `SELECT p.id,m.participant_id FROM pools p
	 JOIN pool_contracts c ON c.pool_id=p.id AND c.revision=p.revision
	 JOIN pool_contract_participants m ON m.contract_id=c.id
	 JOIN participants x ON x.id=m.participant_id AND x.enabled AND x.deleted_at IS NULL
	 WHERE p.enabled AND (p.weekly_wallet->>'enabled'='true' OR p.weekly_wallet->>'auto_allocate'='true')
	 ORDER BY ((m.participant_id,p.id)<=($1,$2)),m.participant_id,p.id LIMIT 1`, afterMember, afterPool).Scan(&pool, &member)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return after, err
	}
	return member + "/" + pool, s.runWeeklyWallet(ctx, pool, member)
}

func (s *Service) runWeeklyWallet(ctx context.Context, pool, participant string) error {
	_, err := s.applyWeeklyWallet(ctx, pool, participant, nil)
	return err
}

func weeklyWalletPending(ctx context.Context, tx pgx.Tx, participant string) (bool, error) {
	var pending bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_application_requests r
	 WHERE r.participant_id=$1 AND NOT (`+walletApplicationResolvedSQL+`))`, participant).Scan(&pending)
	return pending, err
}

func (s *Service) weeklyWalletCandidate(ctx context.Context, tx pgx.Tx, p weeklyWalletPool, member Participant, a storedAccount, c storedConfig, policy MonitoringConfig, manual *weeklyWalletManual) (weeklyWalletEntry, string, string, error) {
	entry, prior, gross, same, err := s.weeklyWalletDue(ctx, tx, p, member, a, c, policy, manual != nil)
	if err != nil || gross == nil {
		return entry, "", "", err
	}
	preview := manual != nil && manual.preview
	grantID, reason, err := s.writeGrant(ctx, tx, c, writeWallet, false)
	if err != nil || reason != "" && !preview {
		entry.State = "write_unavailable"
		return entry, "", "", err
	}
	entry.State = "balance_unavailable"
	kind, secret, err := s.decrypt(c)
	if err != nil {
		return entry, "", "", err
	}
	balance, frozen := s.nativeWalletFields(ctx, c, kind, secret, member.Binding.UpstreamUserID)
	if balance == nil || frozen == nil || *frozen != "0" {
		return entry, "", "", nil
	}
	plan, ok := calculateWeeklyWallet(gross, *balance, prior, same)
	if !ok {
		return entry, "", "", nil
	}
	plan.ContractID, plan.EstimateID, plan.SharePercent = p.ContractID, entry.Plan.EstimateID, entry.Plan.SharePercent
	entry.Plan, entry.ParticipantID, entry.Name = plan, member.ID, member.Name
	entry.RecordedAt = time.Now().UTC().Truncate(time.Microsecond)
	if !entry.RecordedAt.Before(entry.PeriodEnd) {
		entry.State = "waiting_estimate"
		return entry, "", "", nil
	}
	delta := new(big.Rat).Sub(weeklyWalletAmount(plan.Target), weeklyWalletAmount(*balance))
	amount := usageDecimal(delta)
	if grantID != "" && ((delta.Sign() != 0 && !validNativeDelta(amount)) || !s.weeklyWalletWriteAvailable(ctx, c, kind, secret, delta.Sign() > 0)) {
		grantID = ""
	}
	if grantID == "" && !preview {
		entry.State = "write_unavailable"
		return entry, "", "", nil
	}
	entry.State = "ready"
	return entry, amount, grantID, nil
}

func weeklyWalletBasis(p weeklyWalletPool, c storedConfig, member Participant, entry weeklyWalletEntry) string {
	body, _ := json.Marshal([]any{p.ID, p.Revision, p.Control.Revision, p.AccountID, c.SourceID, c.Revision,
		member.ID, member.Revision, member.Binding.ID, entry.PeriodEnd, entry.Plan})
	return fmt.Sprintf("%x", sha256.Sum256(body))
}

func (s *Service) applyWeeklyWallet(ctx context.Context, pool, participant string, manual *weeklyWalletManual) (weeklyWalletEntry, error) {
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
	grouped, err := weeklyWalletGroupRequired(ctx, tx, participant)
	if err != nil {
		return entry, err
	}
	var p weeklyWalletPool
	var c storedConfig
	var member Participant
	var a storedAccount
	var policy MonitoringConfig
	var group weeklyWalletGroupEnvironment
	var ok bool
	if grouped {
		group, ok, err = s.weeklyWalletGroupEnvironment(ctx, tx, participant, manual)
		c, member = group.config, group.member
	} else {
		p, c, member, a, policy, ok, err = s.weeklyWalletEnvironment(ctx, tx, pool, participant, manual)
	}
	if err != nil {
		return entry, err
	}
	if !ok {
		if manual != nil {
			return entry, auth.ErrConflict
		}
		return entry, nil
	}
	pending, err := weeklyWalletPending(ctx, tx, participant)
	if err != nil {
		return entry, err
	}
	if pending {
		if manual != nil {
			return entry, auth.ErrConflict
		}
		return entry, s.recoverWeeklyWallet(ctx, tx, c, member)
	}
	var amount, grantID, basis string
	if grouped {
		cutoff := time.Now().UTC().Truncate(time.Microsecond)
		if manual != nil {
			if manual.cutoff == nil {
				return entry, auth.ErrConflict
			}
			cutoff = manual.cutoff.UTC()
		}
		entry, p, _, grantID, err = s.weeklyWalletGroupCandidate(ctx, tx, group, pool, manual, cutoff, nil)
		if entry.Group != nil {
			amount, basis = entry.Group.Delta, entry.basis
		}
	} else {
		if manual != nil && manual.cutoff != nil {
			return entry, auth.ErrConflict
		}
		entry, amount, grantID, err = s.weeklyWalletCandidate(ctx, tx, p, member, a, c, policy, manual)
		basis = weeklyWalletBasis(p, c, member, entry)
	}
	if err != nil {
		return entry, err
	}
	if manual != nil {
		if entry.State != "ready" || manual.basis != basis {
			return entry, auth.ErrConflict
		}
		if _, err = s.auth.AuthorizeAdminSession(ctx, tx, manual.token); err != nil {
			return entry, err
		}
	}
	if entry.State != "ready" {
		return entry, nil
	}
	var application *WalletApplication
	entry.State = "applied"
	if amount != "0" {
		out, err := makeWeeklyWalletIntent(ctx, tx, p, c, member, entry, amount)
		if err != nil {
			return entry, err
		}
		entry.ApplicationID, application = &out.ID, &out
		entry.State = "pending"
	}
	if grouped {
		err = saveWeeklyWalletGroup(ctx, tx, member.Binding.ID, &entry)
	} else {
		err = tx.QueryRow(ctx, `INSERT INTO weekly_wallet_allocations(pool_id,account_id,participant_id,binding_id,period_end,phase,application_id,recorded_at,snapshot)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id::text`, p.ID, p.AccountID, member.ID, member.Binding.ID, entry.PeriodEnd,
			entry.Phase, entry.ApplicationID, entry.RecordedAt, entry.Plan).Scan(&entry.ID)
	}
	if err != nil {
		return entry, err
	}
	task, outcome := "weekly_wallet", "succeeded"
	if application != nil {
		outcome = "pending"
	}
	event := audit.Entry{
		TaskID: &task, OperationID: "weekly-wallet:" + entry.ID, Action: "pool.weekly_wallet." + entry.Phase,
		Outcome: outcome, TargetType: "participant", TargetID: member.ID, SourceID: c.SourceID,
	}
	if grouped {
		event.OperationID = "weekly-wallet-group:" + entry.ID
	}
	if manual != nil {
		event.ActorID, event.TaskID = &p.actorID, nil
	}
	if _, err = audit.Record(ctx, tx, event); err != nil {
		return entry, err
	}
	if application == nil {
		return entry, tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 1380991564))`, participant); err != nil {
		return entry, err
	}
	defer unlockWalletSession(conn, participant)
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
	entry.State, err = s.executeWeeklyWallet(ctx, conn, p, entry, *application, manual, grantID)
	return entry, err
}

func (s *Service) weeklyWalletEnvironment(ctx context.Context, tx pgx.Tx, pool, participant string, manual *weeklyWalletManual) (weeklyWalletPool, storedConfig, Participant, storedAccount, MonitoringConfig, bool, error) {
	var p weeklyWalletPool
	var member Participant
	var a storedAccount
	var actor auth.Identity
	if manual != nil {
		var err error
		if actor, err = s.auth.AuthorizeAdminSession(ctx, tx, manual.token); err != nil {
			return p, storedConfig{}, member, a, MonitoringConfig{}, false, err
		}
	}
	policy, err := readMonitoringConfig(ctx, tx, false)
	if err != nil || !policy.Enabled {
		return p, storedConfig{}, member, a, policy, false, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return p, c, member, a, policy, false, err
	}
	p, err = s.readWeeklyWalletPool(ctx, tx, c, pool)
	if err != nil || !p.Eligible || p.AccountID == "" || manual == nil && !p.current(c) {
		return p, c, member, a, policy, false, err
	}
	if manual == nil {
		actor, err = s.auth.AuthorizeAutomationAdmin(ctx, tx, p.Control.ActorID, p.Control.ActorRevision)
		if err != nil {
			if errors.Is(err, auth.ErrUnauthorized) || errors.Is(err, auth.ErrForbidden) {
				err = nil
			}
			return p, c, member, a, policy, false, err
		}
	}
	p.actorID = actor.ID
	a, err = readManaged(ctx, tx, p.AccountID, c, false)
	if err != nil {
		return p, c, member, a, policy, false, err
	}
	member, err = readParticipant(ctx, tx, participant, c, false, false)
	if errors.Is(err, auth.ErrNotFound) {
		return p, c, member, a, policy, false, nil
	}
	if err != nil || !member.Enabled || !member.SourceCurrent || member.Binding.EndedAt != nil {
		return p, c, member, a, policy, false, err
	}
	var matched bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pool_contract_participants
	 WHERE contract_id=$1 AND participant_id=$2 AND binding_id=$3)`, p.ContractID, participant, member.Binding.ID).Scan(&matched)
	return p, c, member, a, policy, matched, err
}

func makeWeeklyWalletIntent(ctx context.Context, tx pgx.Tx, p weeklyWalletPool, c storedConfig, member Participant, entry weeklyWalletEntry, delta string) (WalletApplication, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return WalletApplication{}, err
	}
	key := base64.RawURLEncoding.EncodeToString(raw)
	var inputPlan any = entry.Plan
	balanceBefore := entry.Plan.BalanceBefore
	if entry.Group != nil {
		inputPlan = entry.Group
		balanceBefore = entry.Group.BalanceBefore
	}
	plan, err := json.Marshal(inputPlan)
	if err != nil {
		return WalletApplication{}, err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(plan))
	input := WalletApplicationInput{
		WalletExpected: WalletExpected{ParticipantID: member.ID, ParticipantRevision: member.Revision,
			BindingID: member.Binding.ID, SourceID: *c.SourceID, ConnectionRevision: c.Revision, LatestID: "0"},
		RequestID: key, RecommendationID: "0", NativeDelta: delta,
	}
	out := WalletApplication{ParticipantID: member.ID, AttemptedAt: entry.RecordedAt, Visibility: "administrator", WalletApplicationSnapshot: WalletApplicationSnapshot{
		ParticipantName: member.Name, State: "pending", DispatchState: "pending", Reason: "NATIVE_WALLET_ADJUST_PENDING",
		Unit: "native_wallet", UpdatedAt: &entry.RecordedAt,
		Details: &WalletApplicationDetails{ActorID: p.actorID, Input: input, UpstreamUserID: member.Binding.UpstreamUserID,
			EvaluatedBasisHash: digest, Reasons: []string{"NATIVE_WALLET_ADJUST_PENDING"},
			NativeAdjustment: &nativeWalletAdjustment{BalanceBefore: balanceBefore}},
	}}
	err = tx.QueryRow(ctx, `INSERT INTO wallet_application_requests(actor_id,request_id,request_digest,participant_id,binding_id,attempted_at,snapshot)
	 VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id::text`, p.actorID, key, digest, member.ID, member.Binding.ID,
		entry.RecordedAt, out.WalletApplicationSnapshot).Scan(&out.ID)
	return out, err
}

func (s *Service) weeklyWalletWriteAvailable(ctx context.Context, c storedConfig, kind, secret string, credit bool) bool {
	mode, _ := s.upstream.readRunMode(ctx, c.BaseURL, kind, secret)
	if mode == nil || *mode != "standard" {
		return false
	}
	if !credit {
		return true
	}
	body, check := s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/settings", kind, secret, "settings.read", true)
	var fields struct {
		Affiliate *bool `json:"affiliate_enabled"`
		Rebate    *bool `json:"affiliate_admin_recharge_enabled"`
	}
	return check.State == "available" && decode(body, &fields) == nil &&
		(fields.Affiliate != nil && !*fields.Affiliate || fields.Rebate != nil && !*fields.Rebate)
}

func (s *Service) executeWeeklyWallet(ctx context.Context, conn *pgxpool.Conn, expected weeklyWalletPool, entry weeklyWalletEntry, out WalletApplication, manual *weeklyWalletManual, grantID string) (string, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if entry.Group != nil {
		return s.executeWeeklyWalletGroup(ctx, tx, expected, entry, out, manual, grantID)
	}
	p, c, member, a, policy, ok, authorityErr := s.weeklyWalletEnvironment(ctx, tx, expected.ID, out.ParticipantID, manual)
	if authorityErr != nil && !errors.Is(authorityErr, auth.ErrUnauthorized) && !errors.Is(authorityErr, auth.ErrForbidden) {
		return "", authorityErr
	}
	out.State, out.DispatchState, out.Reason = "rejected", "not_dispatched", "NATIVE_WALLET_PRECONDITION_CHANGED"
	grouped, err := weeklyWalletGroupRequired(ctx, tx, out.ParticipantID)
	if err != nil {
		return "", err
	}
	if ok && !grouped && p.ContractID == expected.ContractID && p.Control == expected.Control && p.actorID == expected.actorID &&
		c.SourceID != nil && *c.SourceID == out.Details.Input.SourceID && c.Revision == out.Details.Input.ConnectionRevision && member.Revision == out.Details.Input.ParticipantRevision &&
		member.Binding.ID == out.Details.Input.BindingID && member.Binding.UpstreamUserID == out.Details.UpstreamUserID {
		due, _, _, _, err := s.weeklyWalletDue(ctx, tx, p, member, a, c, policy, manual != nil)
		if err != nil {
			return "", err
		}
		if due.Phase == entry.Phase && due.PeriodEnd.Equal(entry.PeriodEnd) && equalStringPtr(due.Plan.EstimateID, entry.Plan.EstimateID) {
			kind, secret, err := s.decrypt(c)
			if err != nil {
				return "", err
			}
			balance, frozen := s.nativeWalletFields(ctx, c, kind, secret, member.Binding.UpstreamUserID)
			if balance != nil && *balance == entry.Plan.BalanceBefore && frozen != nil && *frozen == "0" &&
				s.weeklyWalletWriteAvailable(ctx, c, kind, secret, out.Details.Input.NativeDelta[0] != '-') {
				err = s.postNativeWalletAdjustment(ctx, tx, c, kind, secret, grantID, &out, func() (bool, error) {
					if manual != nil {
						_, authorityErr = s.auth.AuthorizeAdminSession(ctx, tx, manual.token)
						if authorityErr != nil && !errors.Is(authorityErr, auth.ErrUnauthorized) && !errors.Is(authorityErr, auth.ErrForbidden) {
							return false, authorityErr
						}
					}
					return authorityErr == nil && time.Now().Before(entry.PeriodEnd), nil
				})
				if err != nil {
					return "", err
				}
			}
		}
	}
	out.Details.Reasons = []string{out.Reason}
	actor := ""
	if manual != nil {
		actor = expected.actorID
	}
	if err = saveWalletApplicationResult(ctx, tx, &out, actor, "weekly_wallet"); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return out.State, authorityErr
}

func (s *Service) recoverWeeklyWallet(ctx context.Context, tx pgx.Tx, c storedConfig, member Participant) error {
	out, err := scanWalletApplication(tx.QueryRow(ctx, `SELECT `+walletApplicationColumns+`
	 FROM wallet_application_requests r
	 WHERE r.binding_id=$1 AND r.participant_id=$2 AND r.snapshot->>'state'='pending'
	  AND (EXISTS(SELECT 1 FROM weekly_wallet_allocations w WHERE w.application_id=r.id)
	   OR EXISTS(SELECT 1 FROM weekly_wallet_groups g WHERE g.application_id=r.id))
	  AND NOT EXISTS(SELECT 1 FROM wallet_application_results WHERE application_id=r.id)
	 ORDER BY r.id DESC LIMIT 1`, member.Binding.ID, member.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if out.Details == nil || out.Details.NativeAdjustment == nil || out.Details.Input.SourceID != *c.SourceID ||
		out.Details.UpstreamUserID != member.Binding.UpstreamUserID {
		return nil
	}
	s.observeNativeWalletApplication(ctx, c, &out)
	out.Details.Reasons = []string{out.Reason}
	if err = saveWalletApplicationResult(ctx, tx, &out, "", "weekly_wallet"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func equalStringPtr(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
