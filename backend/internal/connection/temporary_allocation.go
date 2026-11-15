package connection

import (
	"context"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/allocation"
)

// Only a live, matching frozen round replaces ordinary allocation; it does not change contracts.
func (s *Service) temporaryAllocation(ctx context.Context, tx pgx.Tx, c storedConfig, b walletBasis, a walletSource, ledger *simulationLedger, input allocation.Input, expiry *time.Time, participant string, override *TemporaryRightsChange) (allocation.Result, *WalletTemporaryEffect, error) {
	round, err := temporaryLatest(ctx, tx)
	if err != nil {
		return allocation.Result{}, nil, err
	}
	if round != nil {
		exit, err := temporaryEffectiveExit(ctx, tx, *round, time.Now().UTC())
		if err != nil {
			return allocation.Result{}, nil, err
		}
		if exit == nil {
			incomplete := allocation.Result{State: "condition_changed"}
			if round.Scope.SourceID != b.SourceID || round.Scope.ConnectionRevision != b.ConnectionRevision || round.Scope.PolicyRevision != b.PolicyRevision || round.Scope.Simulation.Epoch != s.simulationEpoch(c) {
				return incomplete, nil, nil
			}
			foundWallet := false
			for _, wallet := range round.Scope.Wallets {
				if wallet.ParticipantID != "" && wallet.BindingID == b.BindingID && wallet.ParticipantRevision == b.ParticipantRevision {
					foundWallet = true
				}
			}
			if !foundWallet {
				return incomplete, nil, nil
			}
			for _, frozen := range round.Scope.Accounts {
				if frozen.AccountID != a.AccountID {
					continue
				}
				if !temporaryAccountMatches(frozen, b, a) || !temporaryPeriodsMatch(frozen.Periods, a.Estimate.Windows) {
					return incomplete, nil, nil
				}
				spent := map[string]*big.Rat{}
				owners := map[string]string{}
				for _, member := range frozen.Members {
					spent[member.ParticipantID] = new(big.Rat)
					owners[member.UserID] = member.ParticipantID
				}
				for _, charge := range ledger.Charges {
					if charge.ChargedAt.Before(round.StartedAt) {
						continue
					}
					owner := owners[charge.UserID]
					price := ledger.PricesByVersion[charge.PriceVersion]
					value, ok := simulationDecimal(charge.ActualCost)
					if !ok || owner == "" || price.Version != frozen.PriceVersion || price.WalletMultiplier != frozen.WalletMultiplier || price.BillingType != "standard_wallet" {
						return incomplete, nil, nil
					}
					spent[owner].Add(spent[owner], value)
				}
				members := []allocation.TemporaryMember{}
				var effect *WalletTemporaryEffect
				for _, member := range frozen.Members {
					cap := ""
					for _, row := range round.Caps {
						if row.AccountID == a.AccountID && row.ParticipantID == member.ParticipantID {
							cap = row.Amount
							break
						}
					}
					members = append(members, allocation.TemporaryMember{ID: member.ParticipantID, Cap: cap, Spent: spent[member.ParticipantID].FloatString(8)})
					if member.ParticipantID == participant && cap != "" {
						effect = &WalletTemporaryEffect{State: "active", Mode: round.Mode, Cap: cap, Deadline: &round.Deadline}
					}
				}
				if effect == nil {
					return incomplete, nil, nil
				}
				if round.Deadline.Before(*expiry) {
					*expiry = round.Deadline
				}
				return allocation.Temporary(allocation.TemporaryInput{Remaining: input.Remaining, Members: members, Quantum: simulationWalletQuantum, CostBasis: input.CostBasis, Complete: input.Complete}), effect, nil
			}
			return incomplete, nil, nil
		}
	}
	adjustments, err := temporaryCurrentRights(ctx, tx, s.simulationEpoch(c), b, a)
	if err != nil {
		return allocation.Result{}, nil, err
	}
	if override != nil {
		adjustments[override.ParticipantID] = override.After
	}
	input.Adjustments = adjustments
	effect := &WalletTemporaryEffect{State: "ordinary"}
	if adjustment, ok := temporarySigned(adjustments[participant]); ok && adjustment.Sign() != 0 {
		effect.State, effect.Adjustment = "rights", adjustments[participant]
	}
	return allocation.Evaluate(input), effect, nil
}
