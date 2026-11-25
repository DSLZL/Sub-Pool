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
	"sub-pool/backend/internal/notifications"
)

type nativeWalletFinancialCycle struct {
	weeklyWalletCycle
	ContractID      string `json:"contract_id"`
	AccountRevision string `json:"account_revision"`
	CostVersion     int    `json:"cost_version"`
}

type nativeWalletFinancialSnapshot struct {
	MonitoringRevision string                       `json:"monitoring_revision,omitempty"`
	Cycles             []nativeWalletFinancialCycle `json:"cycles,omitempty"`
	ConfirmedID        string                       `json:"confirmed_id,omitempty"`
	Issuance           string                       `json:"issuance,omitempty"`
	Balance            *string                      `json:"balance,omitempty"`
	Advice             string                       `json:"advice,omitempty"`
	AdviceInputs       [][2]string                  `json:"advice_inputs,omitempty"`
}

type nativeWalletFinancialSample struct {
	ID, Source, Connection, Participant, Policy string
	SampledAt, CheckedAt                        time.Time
	Snapshot                                    nativeWalletFinancialSnapshot
}

const nativeWalletFinancialColumns = `id::text,source_id,connection_revision::text,participant_revision::text,policy_revision::text,sampled_at,checked_at,snapshot`

func readNativeWalletFinancialSample(row pgx.Row) (*nativeWalletFinancialSample, error) {
	var sample nativeWalletFinancialSample
	err := row.Scan(&sample.ID, &sample.Source, &sample.Connection, &sample.Participant, &sample.Policy, &sample.SampledAt, &sample.CheckedAt, &sample.Snapshot)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &sample, err
}

func latestNativeWalletFinancialSample(ctx context.Context, tx pgx.Tx, participant, binding string) (*nativeWalletFinancialSample, error) {
	return readNativeWalletFinancialSample(tx.QueryRow(ctx, `SELECT `+nativeWalletFinancialColumns+` FROM native_wallet_financial_samples
	 WHERE participant_id=$1 AND binding_id=$2 ORDER BY native_wallet_financial_samples.id DESC LIMIT 1`, participant, binding))
}

func (f nativeWalletFinancialSnapshot) accounts() []string {
	ids := make([]string, 0, len(f.Cycles))
	for _, cycle := range f.Cycles {
		ids = append(ids, cycle.AccountID)
	}
	slices.Sort(ids)
	return ids
}

func (f nativeWalletFinancialSnapshot) cycleKey() string {
	cycles := make([][3]string, 0, len(f.Cycles))
	for _, cycle := range f.Cycles {
		cycles = append(cycles, [3]string{cycle.AccountID, cycle.Start.Format(time.RFC3339Nano), cycle.End.Format(time.RFC3339Nano)})
	}
	slices.SortFunc(cycles, func(a, b [3]string) int { return slices.Compare(a[:], b[:]) })
	body, _ := json.Marshal(cycles)
	return fmt.Sprintf("%x", sha256.Sum256(body))
}

func (f nativeWalletFinancialSnapshot) sameBasis(other nativeWalletFinancialSnapshot, compareCost bool) bool {
	return len(f.Cycles) != 0 && f.MonitoringRevision == other.MonitoringRevision && f.ConfirmedID == other.ConfirmedID &&
		f.Issuance == other.Issuance && slices.EqualFunc(f.Cycles, other.Cycles, func(a, b nativeWalletFinancialCycle) bool {
		return a.weeklyWalletAccountKey == b.weeklyWalletAccountKey && a.ContractID == b.ContractID && a.AccountRevision == b.AccountRevision &&
			a.Start.Equal(b.Start) && a.End.Equal(b.End) && (!compareCost || a.CostVersion == b.CostVersion)
	})
}

func nativeWalletBelowIssuance(f nativeWalletFinancialSnapshot) (below, known bool) {
	issued := weeklyWalletAmount(f.Issuance)
	if f.Balance == nil || issued == nil || issued.Sign() <= 0 {
		return false, false
	}
	balance := weeklyWalletAmount(*f.Balance)
	return balance != nil && balance.Mul(balance, big.NewRat(5, 1)).Cmp(issued) < 0, balance != nil
}

func nativeWalletFinancialChanged(kind string, previous, current *nativeWalletFinancialSample, staleAfter time.Duration) bool {
	if previous == nil || current == nil || previous.Source != current.Source || previous.Connection != current.Connection ||
		previous.Participant != current.Participant || previous.Policy != current.Policy || !previous.CheckedAt.Before(current.SampledAt) ||
		current.SampledAt.Sub(previous.CheckedAt) > staleAfter || !previous.Snapshot.sameBasis(current.Snapshot, kind == "recommendation_changed") {
		return false
	}
	switch kind {
	case "balance_low":
		wasBelow, previousKnown := nativeWalletBelowIssuance(previous.Snapshot)
		below, currentKnown := nativeWalletBelowIssuance(current.Snapshot)
		return previousKnown && currentKnown && !wasBelow && below
	case "recommendation_changed":
		before, after := previous.Snapshot.Advice, current.Snapshot.Advice
		return before != "" && after != "" && before != after
	}
	return false
}

func (s *Service) nativeWalletFinancialBasis(ctx context.Context, tx pgx.Tx, env weeklyWalletGroupEnvironment, now time.Time, advice bool) (nativeWalletFinancialSnapshot, error) {
	var out nativeWalletFinancialSnapshot
	origin, err := readWeeklyWalletGroupOrigin(ctx, tx, env.member)
	if err != nil || origin.plan.Cutoff.After(now) {
		return out, err
	}
	confirmed := make(map[weeklyWalletAccountKey]weeklyWalletContribution)
	for _, item := range origin.plan.Contributions {
		if !item.Start.After(now) && now.Before(item.End) {
			confirmed[item.weeklyWalletAccountKey] = item
		}
	}
	issued := new(big.Rat)
	accounts := make(map[string]bool)
	knownIssuance := origin.id != ""
	out.MonitoringRevision, out.ConfirmedID = env.policy.Revision, origin.id
	for _, ref := range env.pools {
		pool, err := s.readWeeklyWalletPool(ctx, tx, env.config, ref.ID)
		if err != nil || !pool.Eligible {
			return nativeWalletFinancialSnapshot{}, err
		}
		var bound bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pool_contract_participants WHERE contract_id=$1 AND participant_id=$2 AND binding_id=$3)`,
			pool.ContractID, env.member.ID, env.member.Binding.ID).Scan(&bound)
		if err != nil || !bound {
			return nativeWalletFinancialSnapshot{}, err
		}
		for _, id := range pool.AccountIDs {
			if accounts[id] || len(accounts) >= 500 {
				return nativeWalletFinancialSnapshot{}, nil
			}
			accounts[id] = true
			account, err := readManaged(ctx, tx, id, env.config, false)
			if err != nil {
				return nativeWalletFinancialSnapshot{}, err
			}
			view, plan, err := readNativeWeeklyPlan(ctx, tx, account, env.config, env.policy, now)
			if err != nil || view == nil || view.Period == nil || view.Observation == nil || view.stale(env.policy, now) ||
				(view.State != "collecting" && view.State != "awaiting_increase") {
				return nativeWalletFinancialSnapshot{}, err
			}
			key := weeklyWalletAccountKey{PoolID: pool.ID, AccountID: id}
			start, end := view.Period.StartAt.Truncate(time.Microsecond), view.Period.EndAt.Truncate(time.Microsecond)
			if prior, found := confirmed[key]; found {
				drift := end.Sub(prior.End)
				if drift < -nativeResetDrift || drift > nativeResetDrift || !view.Observation.At.Before(prior.End) {
					return nativeWalletFinancialSnapshot{}, nil
				}
				start, end = prior.Start, prior.End
				gross, debt := weeklyWalletAmount(prior.Gross), weeklyWalletAmount(prior.OpeningDebt)
				if gross == nil || debt == nil || gross.Sign() < 0 || debt.Sign() < 0 {
					return nativeWalletFinancialSnapshot{}, nil
				}
				issued.Add(issued, new(big.Rat).Sub(gross, debt))
				delete(confirmed, key)
			} else {
				knownIssuance = false
			}
			if now.Before(start) || !now.Before(end) {
				return nativeWalletFinancialSnapshot{}, nil
			}
			out.Cycles = append(out.Cycles, nativeWalletFinancialCycle{
				weeklyWalletCycle: weeklyWalletCycle{key, start.UTC(), end.UTC()},
				ContractID:        pool.ContractID, AccountRevision: account.Revision, CostVersion: plan.CostVersion,
			})
			if advice {
				input := [2]string{view.Observation.ID, ""}
				if len(plan.Points) != 0 {
					_, latest, err := readNativeWeeklyWork(ctx, tx, account.ID, plan.key())
					if err != nil {
						return nativeWalletFinancialSnapshot{}, err
					}
					if latest != nil && plan.includes(latest.Work.Plan) && plan.CostVersion == latest.Work.Plan.CostVersion {
						input[1] = latest.ID
					}
				}
				out.AdviceInputs = append(out.AdviceInputs, input)
			}
		}
	}
	if len(confirmed) != 0 {
		return nativeWalletFinancialSnapshot{}, nil
	}
	if knownIssuance {
		out.Issuance = usageDecimal(issued)
	}
	return out, nil
}

func currentNativeWalletRead(read WalletRead, env weeklyWalletGroupEnvironment) bool {
	return read.BindingID == env.member.Binding.ID && read.SourceID == nativeFinancialSource(env.config) &&
		read.ConnectionRevision == env.config.Revision && read.ParticipantRevision == env.member.Revision
}

func qualifiedNativeWalletRead(read WalletRead) bool {
	return read.ReadAt != nil && read.Balance.Value != nil && read.Balance.State == "reported" &&
		read.FrozenBalance.Value != nil && *read.FrozenBalance.Value == "0" && read.Read.State == "available" && read.FieldAuthority.State == "available"
}

func (s *Service) nativeWalletFinancialAdvice(ctx context.Context, read WalletRead) (nativeWalletFinancialSnapshot, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nativeWalletFinancialSnapshot{}, err
	}
	defer tx.Rollback(ctx)
	env, ok, err := s.weeklyWalletGroupEnvironment(ctx, tx, read.ParticipantID, nil)
	if err != nil || !ok || !currentNativeWalletRead(read, env) {
		return nativeWalletFinancialSnapshot{}, err
	}
	current, err := financialApplicationsCurrent(ctx, tx, &read)
	if err != nil || !current {
		return nativeWalletFinancialSnapshot{}, err
	}
	now := time.Now().UTC()
	facts, err := s.nativeWalletFinancialBasis(ctx, tx, env, now, true)
	if err != nil || len(facts.Cycles) == 0 {
		return facts, err
	}
	grouped, err := weeklyWalletGroupRequired(ctx, tx, read.ParticipantID)
	if err != nil {
		return facts, err
	}
	anchor := env.pools[0].ID
	if grouped {
		entry, _, _, _, err := s.weeklyWalletGroupCandidate(ctx, tx, env, anchor, nil, read.ReadAt.Truncate(time.Microsecond), read.Balance.Value)
		if err == nil && entry.State == "ready" && entry.Group != nil {
			facts.Advice = entry.Group.Target
		}
		return facts, err
	}
	pool, err := s.readWeeklyWalletPool(ctx, tx, env.config, anchor)
	if err != nil || pool.AccountID == "" {
		return facts, err
	}
	account, err := readManaged(ctx, tx, pool.AccountID, env.config, false)
	if err != nil {
		return facts, err
	}
	entry, prior, gross, sameCycle, err := s.weeklyWalletDue(ctx, tx, pool, env.member, account, env.config, env.policy, true)
	if err == nil && now.Before(entry.PeriodEnd) {
		if plan, ok := calculateWeeklyWallet(gross, *read.Balance.Value, prior, sameCycle); ok {
			facts.Advice = plan.Target
		}
	}
	return facts, err
}

func (s *Service) validateNativeWalletNotification(ctx context.Context, tx pgx.Tx, e notifications.FinancialEvidence) (bool, error) {
	if err := lockWallet(ctx, tx, e.SubjectID); err != nil {
		return false, err
	}
	env, ok, err := s.weeklyWalletGroupEnvironment(ctx, tx, e.SubjectID, nil)
	if err != nil || !ok || nativeFinancialSource(env.config) != e.SourceID || env.config.Revision != e.ConnectionRevision ||
		env.member.Revision != e.SubjectRevision || env.member.Binding.ID != e.Native.BindingID {
		return false, err
	}
	now := time.Now().UTC()
	advice := e.Kind == "recommendation_changed"
	facts, err := s.nativeWalletFinancialBasis(ctx, tx, env, now, advice)
	if err != nil || !slices.Equal(facts.accounts(), e.AccountIDs) {
		return false, err
	}
	head, err := latestNativeWalletFinancialSample(ctx, tx, e.SubjectID, e.Native.BindingID)
	staleAfter := time.Duration(env.policy.StaleAfterSeconds) * time.Second
	if err != nil || head == nil || head.Source != e.SourceID || head.Connection != e.ConnectionRevision ||
		head.Participant != e.SubjectRevision || head.Policy != e.Native.PolicyRevision || head.CheckedAt.After(now) ||
		now.Sub(head.CheckedAt) > staleAfter || !head.Snapshot.sameBasis(facts, advice) {
		return false, err
	}
	read := newWalletRead(env.config, env.member)
	read.ReadAt = &head.CheckedAt
	current, err := financialApplicationsCurrent(ctx, tx, &read)
	if err != nil || !current {
		return false, err
	}
	manual, err := latestWalletRead(ctx, tx, e.SubjectID, e.Native.BindingID, false)
	if err != nil || manual != nil && manual.AttemptedAt.After(head.CheckedAt) {
		return false, err
	}
	rows := ` FROM native_wallet_financial_samples WHERE participant_id=$1 AND binding_id=$2 AND id=$3`
	r, err := readNativeWalletFinancialSample(tx.QueryRow(ctx, `SELECT `+nativeWalletFinancialColumns+rows, e.SubjectID, e.Native.BindingID, e.RecordID))
	if err != nil || r == nil || !r.SampledAt.Equal(e.AsOf) || !r.Snapshot.sameBasis(head.Snapshot, advice) || r.Source != head.Source ||
		r.Connection != head.Connection || r.Participant != head.Participant || r.Policy != head.Policy {
		return false, err
	}
	p, err := readNativeWalletFinancialSample(tx.QueryRow(ctx, `SELECT `+nativeWalletFinancialColumns+rows+` AND id<$4`, e.SubjectID, e.Native.BindingID, e.PreviousID, e.RecordID))
	if err != nil || !nativeWalletFinancialChanged(e.Kind, p, r, staleAfter) {
		return false, err
	}
	if e.Kind == "balance_low" {
		below, known := nativeWalletBelowIssuance(head.Snapshot)
		return known && below && e.Native.CycleKey == r.Snapshot.cycleKey(), nil
	}
	return head.Snapshot.Advice == r.Snapshot.Advice && slices.Equal(head.Snapshot.AdviceInputs, facts.AdviceInputs), nil
}
