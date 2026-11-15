package connection

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/allocation"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/estimation"
)

type TemporarySettlement struct {
	RoundID        string                        `json:"round_id"`
	AccountID      string                        `json:"account_id"`
	State          string                        `json:"state"`
	Reason         string                        `json:"reason"`
	OriginalPeriod ObservationPeriod             `json:"original_period"`
	NextPeriod     *ObservationPeriod            `json:"next_period"`
	LedgerRevision string                        `json:"ledger_revision"`
	ClosedThrough  *time.Time                    `json:"closed_through"`
	RoundSpent     map[string]string             `json:"round_spent"`
	PeriodSpent    map[string]string             `json:"period_spent"`
	Adjustments    map[string]string             `json:"adjustments"`
	Residuals      map[string]*estimation.Amount `json:"residuals"`
}

func temporarySettlement(ctx context.Context, tx pgx.Tx, round, account string) (*TemporarySettlement, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT snapshot FROM temporary_events WHERE round_id=$1 AND account_id=$2 AND kind='settlement' ORDER BY id DESC LIMIT 1`, round, account).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out TemporarySettlement
	err = json.Unmarshal(raw, &out)
	return &out, err
}
func temporarySettlementDone(state string) bool {
	return state == "settled" || state == "no_carry" || state == "no_borrowing" || state == "cancelled"
}
func temporarySettlementsFinished(ctx context.Context, tx pgx.Tx, r TemporaryRound) (bool, error) {
	for _, a := range r.Scope.Accounts {
		row, err := temporarySettlement(ctx, tx, r.ID, a.AccountID)
		if err != nil {
			return false, err
		}
		if row == nil || !temporarySettlementDone(row.State) {
			return false, nil
		}
	}
	return true, nil
}
func temporarySaveSettlement(ctx context.Context, tx pgx.Tx, out TemporarySettlement) error {
	raw, err := json.Marshal(out)
	if err != nil {
		return err
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO temporary_events(round_id,account_id,kind,request_key,recorded_at,snapshot) VALUES($1,$2,'settlement',$3,$4,$5) ON CONFLICT(request_key) DO NOTHING RETURNING id::text`, out.RoundID, out.AccountID, "temporary-settlement:"+out.RoundID+":"+out.AccountID+":"+temporaryHash(out), time.Now().UTC(), raw).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	outcome := "unknown"
	if out.State == "pending_evidence" {
		outcome = "pending"
	} else if temporarySettlementDone(out.State) {
		outcome = "succeeded"
	}
	task := "temporary:" + out.RoundID
	_, err = audit.Record(ctx, tx, audit.Entry{OperationID: "temporary-settlement:" + id, TaskID: &task, Action: "temporary.settlement", Outcome: outcome, TargetType: "account", TargetID: out.AccountID})
	return err
}

// The bottleneck window owns one account settlement; overlapping windows remain exit conditions, never additional capacity.
func (s *Service) temporarySettleAccount(ctx context.Context, tx pgx.Tx, c storedConfig, r TemporaryRound, a TemporaryAccount) (TemporarySettlement, error) {
	out := TemporarySettlement{RoundID: r.ID, AccountID: a.AccountID, State: "pending_evidence", Reason: "ORIGINAL_PERIOD_TAIL_UNVERIFIED", OriginalPeriod: a.SettlementPeriod, RoundSpent: map[string]string{}, PeriodSpent: map[string]string{}, Adjustments: map[string]string{}, Residuals: map[string]*estimation.Amount{}}
	if r.Exit == nil {
		return out, nil
	}
	if r.Exit.Reason == "terminated" {
		out.State, out.Reason = "cancelled", "EARLY_TERMINATION"
		return out, nil
	}
	if r.Mode == "no_carry" {
		out.State, out.Reason = "no_carry", "NO_NEW_CARRY_RIGHTS"
		return out, nil
	}
	now := time.Now().UTC()
	if now.Before(a.SettlementPeriod.EndAt) || a.SettlementPeriod.EndAt.IsZero() {
		return out, nil
	}
	if c.SourceID == nil || *c.SourceID != r.Scope.SourceID || s.simulationEpoch(c) != r.Scope.Simulation.Epoch || c.Revision != r.Scope.ConnectionRevision {
		out.Reason = "FROZEN_SOURCE_CHANGED"
		return out, nil
	}
	grants := map[string]*estimation.Amount{}
	var reference *simulationWalletSourceProof
	for _, m := range a.Members {
		basis, err := s.walletBasis(ctx, tx, m.ParticipantID)
		if err != nil {
			return out, err
		}
		var source *walletSource
		for i := range basis.Sources {
			if basis.Sources[i].AccountID == a.AccountID {
				source = &basis.Sources[i]
			}
		}
		if source == nil || basis.BindingID != m.BindingID || basis.SourceID != r.Scope.SourceID || !temporaryAccountMatches(a, basis, *source) {
			out.Reason = "FROZEN_IDENTITY_OR_CONTRACT_CHANGED"
			return out, nil
		}
		var next *ObservationPeriod
		for _, w := range source.Estimate.Windows {
			if w.Kind == a.SettlementPeriod.Kind {
				next = w.Period
			}
		}
		if next == nil || !next.StartAt.Equal(a.SettlementPeriod.EndAt) || !now.Before(next.EndAt) {
			out.Reason = "IMMEDIATE_NEXT_PERIOD_UNVERIFIED"
			return out, nil
		}
		if out.NextPeriod == nil {
			copy := *next
			out.NextPeriod = &copy
		} else if !temporarySamePeriod(*out.NextPeriod, *next) {
			return out, nil
		}
		latest, err := latestWalletRecommendation(ctx, tx, m.ParticipantID, m.BindingID)
		if err != nil {
			return out, err
		}
		current := s.simulationWalletCurrent(c, basis, m.ParticipantID, now, latest)
		if !current.Complete {
			return out, nil
		}
		found := false
		for _, row := range current.Contributions {
			if row.AccountID != a.AccountID {
				continue
			}
			proof := row.SimulationProof
			if row.Amount == nil || proof == nil || proof.ClosedThrough.Before(a.SettlementPeriod.EndAt) {
				return out, nil
			}
			if reference == nil {
				reference = proof
			} else if reference.LedgerRevision != proof.LedgerRevision || !reference.ClosedThrough.Equal(proof.ClosedThrough) {
				return out, nil
			}
			grants[m.ParticipantID] = row.Amount
			found = true
		}
		if !found {
			return out, nil
		}
	}
	managed, err := readManaged(ctx, tx, a.AccountID, c, false)
	if err != nil {
		return out, err
	}
	ledger, state := s.simulationLedger(ctx, c, managed, r.StartedAt, a.SettlementPeriod.EndAt)
	if ledger == nil {
		out.Reason = state
		return out, nil
	}
	if reference == nil || ledger.LedgerRevision != reference.LedgerRevision || !ledger.ClosedThrough.Equal(reference.ClosedThrough) {
		out.Reason = "SETTLEMENT_LEDGER_CHANGED"
		return out, nil
	}
	out.LedgerRevision = ledger.LedgerRevision
	closed := ledger.ClosedThrough
	out.ClosedThrough = &closed
	owners := map[string]string{}
	roundSpent, periodSpent := map[string]*big.Rat{}, map[string]*big.Rat{}
	for _, m := range a.Members {
		owners[m.UserID] = m.ParticipantID
		roundSpent[m.ParticipantID] = new(big.Rat)
		periodSpent[m.ParticipantID] = new(big.Rat)
	}
	for _, charge := range ledger.Charges {
		owner := owners[charge.UserID]
		price := ledger.PricesByVersion[charge.PriceVersion]
		value, ok := simulationDecimal(charge.ActualCost)
		if owner == "" || !ok || price.Version != a.PriceVersion || price.WalletMultiplier != a.WalletMultiplier || price.BillingType != "standard_wallet" {
			out.Reason = "FROZEN_ATTRIBUTION_OR_PRICE_CHANGED"
			return out, nil
		}
		periodSpent[owner].Add(periodSpent[owner], value)
		if charge.ChargedAt.Before(r.Exit.ExitedAt) {
			roundSpent[owner].Add(roundSpent[owner], value)
		}
	}
	input := allocation.CarryInput{Mode: r.Mode, CostBasis: "wallet_deducted_usd", Quantum: simulationWalletQuantum, Complete: true}
	for _, m := range a.Members {
		round, period := roundSpent[m.ParticipantID].FloatString(8), periodSpent[m.ParticipantID].FloatString(8)
		out.RoundSpent[m.ParticipantID], out.PeriodSpent[m.ParticipantID] = round, period
		input.Members = append(input.Members, allocation.CarryMember{ID: m.ParticipantID, BaselineExact: m.Baseline, RoundSpent: round, PeriodSpent: period, NextEntitlementExact: grants[m.ParticipantID]})
	}
	result := allocation.Carry(input)
	out.Reason = result.State
	if result.State == "scenario" {
		out.State = "settled"
		out.Residuals = result.Residuals
		for id, value := range result.Adjustments {
			rational, ok := new(big.Rat).SetString(value.Numerator + "/" + value.Denominator)
			if !ok || !new(big.Rat).Mul(rational, big.NewRat(100000000, 1)).IsInt() {
				out.State, out.Reason = "pending_evidence", "RIGHTS_QUANTUM_UNVERIFIED"
				out.Adjustments = map[string]string{}
				return out, nil
			}
			out.Adjustments[id] = rational.FloatString(8)
		}
	} else if temporarySettlementDone(result.State) {
		out.State = result.State
	}
	return out, nil
}

func (s *Service) processTemporary(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, temporaryLock); err != nil {
		return err
	}
	r, err := temporaryLatest(ctx, tx)
	if err != nil || r == nil {
		return err
	}
	exit, err := temporaryEffectiveExit(ctx, tx, *r, time.Now().UTC())
	if err != nil {
		return err
	}
	if err = temporaryPersistExit(ctx, tx, r, exit); err != nil {
		return err
	}
	if r.Exit == nil {
		if err = temporaryRecordReminders(ctx, tx, *r); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return err
	}
	network, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	for _, a := range r.Scope.Accounts {
		old, err := temporarySettlement(ctx, tx, r.ID, a.AccountID)
		if err != nil {
			return err
		}
		if old != nil && temporarySettlementDone(old.State) {
			continue
		}
		current, err := s.temporarySettleAccount(network, tx, c, *r, a)
		if err != nil {
			return err
		}
		if err = temporarySaveSettlement(ctx, tx, current); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
