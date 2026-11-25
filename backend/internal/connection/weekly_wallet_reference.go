package connection

import (
	"context"
	"errors"
	"math/big"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"sub-pool/backend/internal/estimation"
)

const weeklyWalletReferenceLimit = 500

type weeklyWalletReference struct {
	Amount   *string                        `json:"amount"`
	Accounts []weeklyWalletReferenceAccount `json:"accounts"`
}

type weeklyWalletReferenceAccount struct {
	weeklyWalletAccountKey
	Name         string     `json:"name"`
	SharePercent *string    `json:"share_percent"`
	Amount       *string    `json:"amount"`
	BasisAt      *time.Time `json:"basis_at"`
	UsedPercent  *string    `json:"used_percent"`
	Samples      int        `json:"samples"`
	Reason       string     `json:"reason"`
}

func weeklyWalletReferenceCheckpoints(work nativeWeeklyWork) []NativeWeeklyCheckpoint {
	if len(work.Costs) != len(work.Plan.Points) || len(work.Tail) != 0 && len(work.Tail) != len(work.Costs) {
		return nil
	}
	var total nativeWeeklyCost
	var checkpoints []NativeWeeklyCheckpoint
	for i, point := range work.Plan.Points {
		pieces := []nativeWeeklyCost{work.Costs[i]}
		if len(work.Tail) != 0 {
			pieces = append(pieces, work.Tail[i])
		}
		for _, cost := range pieces {
			_, valid := cost.WalletCost.Rational()
			if cost.Missing || cost.Records < 0 || cost.WalletRows < 0 || cost.WalletRows > cost.Records || cost.WalletRows > 0 && !valid {
				return checkpoints
			}
			cost.Cost = nil
			if cost.WalletRows == 0 {
				cost.WalletCost = nil
			}
			total.add(cost)
		}
		actual, valid := total.WalletCost.Rational()
		used := weeklyWalletAmount(point.UsedPercent)
		if valid && actual.Sign() > 0 && total.WalletRows == total.Records && used != nil && used.Sign() > 0 && used.Cmp(big.NewRat(100, 1)) <= 0 {
			capacity := new(big.Rat).Quo(new(big.Rat).Mul(actual, big.NewRat(100, 1)), used)
			checkpoints = append(checkpoints, NativeWeeklyCheckpoint{Observation: point, Capacity: estimation.Exact(capacity)})
		}
	}
	return checkpoints
}

func (s *Service) readWeeklyWalletReference(ctx context.Context, tx pgx.Tx, c storedConfig, policy MonitoringConfig, member Participant, pools []weeklyWalletRef) (*weeklyWalletReference, error) {
	out := &weeklyWalletReference{}
	now := time.Now().UTC()
	total, complete := new(big.Rat), true
	seen := make(map[string]bool)
	for _, pool := range pools {
		p, err := s.readWeeklyWalletPool(ctx, tx, c, pool.ID)
		if err != nil {
			return nil, err
		}
		var units int64
		err = tx.QueryRow(ctx, `SELECT share_units FROM pool_contract_participants
		 WHERE contract_id=$1 AND participant_id=$2 AND binding_id=$3`, p.ContractID, member.ID, member.Binding.ID).Scan(&units)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		memberCurrent := err == nil && units >= 0 && units <= shareTotal
		if len(p.AccountIDs) == 0 {
			complete = false
		}
		for _, id := range p.AccountIDs {
			if len(out.Accounts) == weeklyWalletReferenceLimit {
				return nil, nil
			}
			row := weeklyWalletReferenceAccount{
				weeklyWalletAccountKey: weeklyWalletAccountKey{PoolID: p.ID, AccountID: id},
				Name:                   id, Reason: "account_unavailable",
			}
			duplicate := seen[id]
			seen[id] = true
			if !memberCurrent || duplicate {
				row.Reason = "condition_changed"
			} else if slices.Contains(p.ReadyAccounts, id) {
				account, err := readManaged(ctx, tx, id, c, false)
				if err != nil {
					return nil, err
				}
				if err = readWeeklyWalletReferenceAccount(ctx, tx, c, policy, account, units, now, &row); err != nil {
					return nil, err
				}
			}
			if row.Amount == nil {
				complete = false
			} else {
				total.Add(total, weeklyWalletAmount(*row.Amount))
			}
			out.Accounts = append(out.Accounts, row)
		}
	}
	if len(out.Accounts) == 0 {
		return nil, nil
	}
	if complete {
		amount := usageDecimal(total)
		out.Amount = &amount
	}
	return out, nil
}

func readWeeklyWalletReferenceAccount(ctx context.Context, tx pgx.Tx, c storedConfig, policy MonitoringConfig, account storedAccount, units int64, now time.Time, row *weeklyWalletReferenceAccount) error {
	share := sharePercent(units)
	row.Name, row.SharePercent = account.Name, &share
	view, plan, err := readNativeWeeklyPlan(ctx, tx, account, c, policy, now)
	if err != nil || view == nil {
		return err
	}
	switch view.State {
	case "source_stale", "condition_changed":
		row.Reason = view.State
	case "period_unknown", "invalid_evidence", "quota_decreased", "observation_limit":
		row.Reason = "period_unavailable"
	case "awaiting_observation", "awaiting_increase", "collecting":
		row.Reason = "waiting_estimate"
	}
	if len(plan.Points) == 0 {
		return nil
	}
	_, latest, err := readNativeWeeklyWork(ctx, tx, account.ID, plan.key())
	if err != nil || latest == nil {
		return err
	}
	if latest.Work.Plan.Period == nil || !latest.Work.Plan.Period.StartAt.Equal(plan.Period.StartAt) {
		row.Reason = "period_changed"
		return nil
	}
	if !plan.includes(latest.Work.Plan) || latest.Work.Plan.CostVersion != plan.CostVersion {
		row.Reason = "condition_changed"
		return nil
	}
	points := weeklyWalletReferenceCheckpoints(latest.Work)
	row.Samples = min(len(points), 3)
	row.Reason = "waiting_actual_cost"
	if len(points) == 0 {
		return nil
	}
	last := points[len(points)-1]
	capacity, _ := last.Capacity.Rational()
	amount := usageDecimal(weeklyWalletFloor(capacity.Mul(capacity, big.NewRat(units, shareTotal))))
	row.Amount, row.BasisAt, row.UsedPercent = &amount, &last.Observation.At, &last.Observation.UsedPercent
	if last.Observation.UsedPercent != latest.Result.Observation.UsedPercent {
		return nil
	}

	standard := latest.Result.Cumulative
	switch {
	case last.Observation.UsedPercent != view.Observation.UsedPercent:
		row.Reason = "waiting_latest_estimate"
	case len(points) < 3 || standard == nil || len(standard.Checkpoints) < 3:
		row.Reason = "collecting_samples"
	default:
		switch weeklyConfidence(points[len(points)-3:]) {
		case "collecting":
			row.Reason = "below_minimum_usage"
		case "unstable":
			row.Reason = "unstable_estimate"
		default:
			if standard.Confidence == "unstable" {
				row.Reason = "unstable_estimate"
			} else if standard.Confidence == "trusted" && latest.Work.WalletCapacity != nil {
				row.Reason = "ready"
			}
		}
	}
	return nil
}
