package connection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/allocation"
	"sub-pool/backend/internal/estimation"
)

// This scope is an explicit simulator claim, never inferred from native user/profile fields.
type simulationWalletScope struct {
	SimulationProvenance
	UserID           string    `json:"user_id"`
	Balance          string    `json:"balance"`
	FrozenBalance    string    `json:"frozen_balance"`
	WalletVersion    string    `json:"wallet_version"`
	LedgerRevision   string    `json:"ledger_revision"`
	ClosedThrough    time.Time `json:"closed_through"`
	Pending          string    `json:"pending_requests"`
	SourceAccountIDs []string  `json:"source_account_ids"`
	ScopeComplete    bool      `json:"scope_complete"`
	BillingType      string    `json:"billing_type"`
	LedgerClosed     bool      `json:"ledger_closed"`
}

type simulationWalletSourceProof struct {
	InputHash      string             `json:"input_hash"`
	StartAt        time.Time          `json:"start_at"`
	EndAt          time.Time          `json:"end_at"`
	ClosedThrough  time.Time          `json:"closed_through"`
	ExpiresAt      time.Time          `json:"expires_at"`
	LedgerRevision string             `json:"ledger_revision"`
	Consumed       *estimation.Amount `json:"consumed"`
	Spent          *estimation.Amount `json:"spent"`
	Remaining      *estimation.Amount `json:"remaining"`
	Reserve        *estimation.Amount `json:"reserve"`
}

func (s *Service) readSimulationWallet(ctx context.Context, c storedConfig, p Participant, out *WalletRead) {
	expected := SimulationProvenance{Protocol: simulationProtocol, Environment: "simulated", Epoch: s.simulationEpoch(c)}
	out.Simulation, out.Unit = &expected, "simulated_wallet_usd"
	out.Read = unknown("users.read", "INVALID_RESPONSE")
	out.FieldAuthority = unknown("users.balance.read", "FIELD_PERMISSION_UNVERIFIED")
	out.Balance, out.FrozenBalance = walletField(nil, false, false), walletField(nil, true, false)
	kind, secret, err := s.decrypt(c)
	if err != nil {
		return
	}
	body, check := s.upstream.get(ctx, c.BaseURL, simulationPrefix+"/wallets/"+p.Binding.UpstreamUserID, kind, secret, "users.read", true)
	out.Read = check
	var scope simulationWalletScope
	valid := check.State == "available" && simulationObject(body, "protocol environment epoch user_id balance frozen_balance wallet_version ledger_revision closed_through pending_requests source_account_ids scope_complete billing_type ledger_closed", &scope)
	fields, _ := usageObject(body)
	balance, balanceOK := simulationDecimal(scope.Balance)
	frozen, frozenOK := simulationDecimal(scope.FrozenBalance)
	valid = valid && scope.SimulationProvenance == expected && scope.UserID == p.Binding.UpstreamUserID && validRevision(scope.WalletVersion) && validRevision(scope.LedgerRevision) && simulationTime(fields["closed_through"]) && !scope.ClosedThrough.After(time.Now()) && validUsageAfter(scope.Pending) && balanceOK && frozenOK && len(scope.SourceAccountIDs) <= 500 && (scope.BillingType == "standard_wallet" || scope.BillingType == "unqualified")
	seen := map[string]bool{}
	for _, id := range scope.SourceAccountIDs {
		valid = valid && validRevision(id) && !seen[id]
		seen[id] = true
	}
	if !valid {
		if check.State == "available" {
			out.Read = invalid(check)
		}
		return
	}
	now := time.Now().UTC()
	out.ReadAt, out.SimulationScope = &now, &scope
	out.FieldAuthority = check
	out.FieldAuthority.ID, out.FieldAuthority.Reason = "users.balance.read", "SIMULATED_WALLET_SCOPE"
	b, f := balance.FloatString(8), frozen.FloatString(8)
	out.Balance = WalletField{Value: &b, State: "reported", Reason: "SIMULATED_FIELD_REPORTED"}
	out.FrozenBalance = WalletField{Value: &f, State: "reported", Reason: "SIMULATED_FIELD_REPORTED"}
}

func walletSourceHash(b walletBasis, a walletSource) string {
	b.WalletHead, b.Sources = "", []walletSource{a}
	body, _ := json.Marshal(b)
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:])
}

// Current queries validate frozen inputs locally and never call the upstream.
func (s *Service) simulationWalletCurrent(c storedConfig, b walletBasis, id string, now time.Time, latest *WalletRecommendation) WalletRecommendationSnapshot {
	out := walletRecommendation(b, id, now)
	epoch := s.simulationEpoch(c)
	if latest == nil || epoch == "" {
		return out
	}
	for i, a := range b.Sources {
		if i >= len(out.Contributions) {
			break
		}
		for _, saved := range latest.Contributions {
			proof := saved.SimulationProof
			if saved.AccountID == a.AccountID && proof != nil && saved.Simulation != nil && saved.Simulation.Protocol == simulationProtocol && saved.Simulation.Environment == "simulated" && saved.Simulation.Epoch == epoch && proof.InputHash == walletSourceHash(b, a) && now.Before(proof.ExpiresAt) && out.Contributions[i].State == "evidence_incomplete" && out.Contributions[i].EstimateState == "scenario" && a.CollectionAvailable {
				out.Contributions[i] = saved
			}
		}
	}
	if latest.Complete && latest.Simulation != nil && latest.Simulation.Epoch == epoch && latest.BasisHash == out.BasisHash && latest.ExpiresAt != nil && now.Before(*latest.ExpiresAt) && len(out.Contributions) > 0 {
		complete := true
		for _, row := range out.Contributions {
			complete = complete && row.State == "scenario" && row.Amount != nil
		}
		if complete {
			out.Global, out.Target, out.Complete, out.Simulation, out.ExpiresAt = latest.Global, latest.Target, true, latest.Simulation, latest.ExpiresAt
		}
	}
	return out
}

type simulationWalletMember struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Share     int64     `json:"share"`
	Current   bool      `json:"current"`
	StartedAt time.Time `json:"started_at"`
}
type simulationWalletContract struct {
	ID          string                   `json:"id"`
	Enabled     bool                     `json:"enabled"`
	EffectiveAt time.Time                `json:"effective_at"`
	EndedAt     *time.Time               `json:"ended_at"`
	Members     []simulationWalletMember `json:"members"`
}

func simulationShares(c simulationWalletContract) (map[string]string, map[string]string, bool) {
	shares, owners := map[string]string{}, map[string]string{}
	var total int64
	for _, m := range c.Members {
		if !m.Current || m.StartedAt.After(c.EffectiveAt) || !managedID(m.ID) || !validRevision(m.UserID) || m.Share < 0 || m.Share > shareTotal || owners[m.UserID] != "" {
			return nil, nil, false
		}
		shares[m.ID], owners[m.UserID] = new(big.Rat).SetFrac64(m.Share, shareTotal).FloatString(8), m.ID
		total += m.Share
	}
	return shares, owners, c.Enabled && len(c.Members) <= 500 && total <= shareTotal
}

func (s *Service) simulationWalletAllocation(ctx context.Context, tx pgx.Tx, c storedConfig, b walletBasis, a walletSource, scope *simulationWalletScope, participant string, seenRequests, seenSequences map[string]bool) (allocation.Result, *simulationWalletSourceProof, string, error) {
	incomplete := allocation.Result{State: "evidence_incomplete"}
	if a.Estimate == nil || a.Estimate.Remaining == nil || !a.Estimate.Complete || len(a.Estimate.Windows) == 0 {
		return incomplete, nil, "evidence_incomplete", nil
	}
	var start, end time.Time
	var currentPrice *SimulationCostProof
	now := time.Now().UTC()
	expiry := scope.ClosedThrough.Add(time.Duration(b.StaleAfterSeconds) * time.Second)
	for _, w := range a.Estimate.Windows {
		if w.State != "scenario" || w.Period == nil || w.SourceAt == nil || w.CostProof == nil || w.CostProof.SimulationProvenance != scope.SimulationProvenance || w.CostProof.BillingType != "standard_wallet" || len(w.CostProof.Intervals) != 2 {
			return incomplete, nil, "evidence_incomplete", nil
		}
		if start.IsZero() || w.Period.StartAt.Before(start) {
			start = w.Period.StartAt
		}
		if end.IsZero() {
			end = *w.SourceAt
		} else if !end.Equal(*w.SourceAt) {
			return incomplete, nil, "condition_changed", nil
		}
		if currentPrice == nil {
			currentPrice = w.CostProof
		} else if currentPrice.PriceVersion != w.CostProof.PriceVersion || currentPrice.WalletMultiplier != w.CostProof.WalletMultiplier || currentPrice.StandardRate != w.CostProof.StandardRate || !currentPrice.PriceEffectiveAt.Equal(w.CostProof.PriceEffectiveAt) {
			return incomplete, nil, "condition_changed", nil
		}
		for _, at := range []*time.Time{w.SourceAt, w.OldestSampleAt} {
			if at == nil || at.After(now) {
				return incomplete, nil, "evidence_incomplete", nil
			}
			expires := at.Add(time.Duration(b.StaleAfterSeconds) * time.Second)
			if expires.Before(expiry) {
				expiry = expires
			}
		}
		if w.Period.EndAt.Before(expiry) {
			expiry = w.Period.EndAt
		}
	}
	if !now.Before(expiry) || !start.Before(end) || scope.ClosedThrough.Before(end) {
		return incomplete, nil, "evidence_incomplete", nil
	}
	managed, err := readManaged(ctx, tx, a.AccountID, c, false)
	if err != nil {
		return incomplete, nil, "", err
	}
	ledger, state := s.simulationLedger(ctx, c, managed, start, scope.ClosedThrough)
	if ledger == nil {
		return incomplete, nil, state, nil
	}
	if ledger.LedgerRevision != scope.LedgerRevision || !ledger.ClosedThrough.Equal(scope.ClosedThrough) {
		return incomplete, nil, "condition_changed", nil
	}
	if len(ledger.Charges) > 2000 {
		return incomplete, nil, "evidence_limit", nil
	}
	for _, charge := range ledger.Charges {
		if seenRequests[charge.RequestID] || seenSequences[charge.Seq] {
			return incomplete, nil, "condition_changed", nil
		}
		seenRequests[charge.RequestID], seenSequences[charge.Seq] = true, true
	}
	// Reconcile the frozen empirical intervals against the complete period ledger.
	for _, w := range a.Estimate.Windows {
		for _, interval := range w.CostProof.Intervals {
			standard, actual, count := new(big.Rat), new(big.Rat), 0
			for _, charge := range ledger.Charges {
				if !charge.ChargedAt.Before(interval.StartAt) && charge.ChargedAt.Before(interval.EndAt) {
					std, _ := simulationDecimal(charge.StandardCost)
					act, _ := simulationDecimal(charge.ActualCost)
					standard.Add(standard, std)
					actual.Add(actual, act)
					count++
					price := ledger.PricesByVersion[charge.PriceVersion]
					if price.Version != currentPrice.PriceVersion || price.StandardRate != currentPrice.StandardRate || price.WalletMultiplier != currentPrice.WalletMultiplier || !price.EffectiveAt.Equal(currentPrice.PriceEffectiveAt) {
						return incomplete, nil, "condition_changed", nil
					}
				}
			}
			std, okS := interval.StandardCost.Rational()
			act, okA := interval.ActualCost.Rational()
			if !okS || !okA || count != interval.EntryCount || standard.Cmp(std) != 0 || actual.Cmp(act) != 0 {
				return incomplete, nil, "condition_changed", nil
			}
		}
	}
	var contracts []simulationWalletContract
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(v.body ORDER BY v.id),'[]'::jsonb) FROM (
 SELECT pc.id,jsonb_build_object('id',pc.id::text,'enabled',pc.enabled,'effective_at',pc.effective_at,'ended_at',pc.ended_at,
 'members',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',cp.participant_id,'user_id',pb.upstream_user_id::text,'share',cp.share_units,'started_at',pb.started_at,
 'current',pb.ended_at IS NULL AND pp.deleted_at IS NULL AND pp.enabled AND pb.source_id=$2) ORDER BY cp.participant_id)
 FROM pool_contract_participants cp JOIN participant_bindings pb ON pb.id=cp.binding_id JOIN participants pp ON pp.id=cp.participant_id WHERE cp.contract_id=pc.id),'[]'::jsonb)) AS body
 FROM pool_contract_accounts ca JOIN pool_contracts pc ON pc.id=ca.contract_id WHERE ca.account_id=$1 AND ca.source_id=$2 AND pc.source_id=$2 AND ca.resource_fingerprint=$3
 ORDER BY pc.id LIMIT 2001) v`, a.AccountID, b.SourceID, managed.fingerprint).Scan(&raw)
	if err != nil {
		return incomplete, nil, "", err
	}
	if err = json.Unmarshal(raw, &contracts); err != nil {
		return incomplete, nil, "", err
	}
	if len(contracts) > 2000 {
		return incomplete, nil, "evidence_limit", nil
	}
	input := allocation.Input{Complete: true, CostBasis: "wallet_deducted_usd", Spent: map[string]string{participant: "0"}, Current: map[string]string{}}
	found := false
	for _, contract := range contracts {
		if a.Contract != nil && contract.ID == a.Contract.ID {
			var valid bool
			input.Current, _, valid = simulationShares(contract)
			if !valid {
				return incomplete, nil, "contract_unavailable", nil
			}
			found = true
		}
	}
	if !found {
		return incomplete, nil, "contract_unavailable", nil
	}
	spent := map[string]*big.Rat{participant: new(big.Rat)}
	for _, charge := range ledger.Charges {
		if ctx.Err() != nil {
			return incomplete, nil, "evidence_incomplete", nil
		}
		if !charge.ChargedAt.Before(end) || ledger.PricesByVersion[charge.PriceVersion].BillingType != "standard_wallet" {
			return incomplete, nil, "condition_changed", nil
		}
		var matching *simulationWalletContract
		for i := range contracts {
			candidate := &contracts[i]
			if !charge.ChargedAt.Before(candidate.EffectiveAt) && (candidate.EndedAt == nil || charge.ChargedAt.Before(*candidate.EndedAt)) {
				if matching != nil {
					return incomplete, nil, "contract_unavailable", nil
				}
				matching = candidate
			}
		}
		if matching == nil {
			return incomplete, nil, "contract_unavailable", nil
		}
		shares, owners, valid := simulationShares(*matching)
		owner := owners[charge.UserID]
		if !valid || owner == "" {
			return incomplete, nil, "contract_unavailable", nil
		}
		input.Segments = append(input.Segments, allocation.Segment{Cost: charge.ActualCost, Shares: shares})
		cost, _ := simulationDecimal(charge.ActualCost)
		if spent[owner] == nil {
			spent[owner] = new(big.Rat)
		}
		spent[owner].Add(spent[owner], cost)
	}
	for id, cost := range spent {
		input.Spent[id] = cost.FloatString(8)
	}
	remaining, valid := a.Estimate.Remaining.Rational()
	multiplier, multiplierOK := simulationDecimal(currentPrice.WalletMultiplier)
	if !valid || !multiplierOK {
		return incomplete, nil, "evidence_incomplete", nil
	}
	input.Remaining = estimation.Exact(new(big.Rat).Mul(remaining, multiplier))
	result := allocation.Evaluate(input)
	if result.State != "scenario" {
		return result, nil, result.State, nil
	}
	consumed, _ := simulationDecimal(ledger.ActualCost)
	proof := &simulationWalletSourceProof{InputHash: walletSourceHash(b, a), StartAt: start, EndAt: end, ClosedThrough: scope.ClosedThrough, ExpiresAt: expiry, LedgerRevision: scope.LedgerRevision, Consumed: estimation.Exact(consumed), Spent: estimation.Exact(spent[participant]), Remaining: input.Remaining, Reserve: result.Reserve}
	return result, proof, "", nil
}

func (s *Service) qualifySimulationWallet(ctx context.Context, tx pgx.Tx, b walletBasis, participant string, out *WalletRecommendationSnapshot) error {
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return err
	}
	epoch := s.simulationEpoch(c)
	if epoch == "" || !b.Enabled || b.CurrentSource == nil || *b.CurrentSource != b.SourceID || len(b.Sources) == 0 || len(b.Sources) > 500 {
		return nil
	}
	read, err := latestWalletRead(ctx, tx, participant, b.BindingID, false)
	if err != nil {
		return err
	}
	if read == nil || read.ID != b.WalletHead || read.SimulationScope == nil || read.ConnectionRevision != b.ConnectionRevision || read.SourceID != b.SourceID || read.Read.State != "available" || read.ReadAt == nil {
		return nil
	}
	scope := read.SimulationScope
	expected := SimulationProvenance{Protocol: simulationProtocol, Environment: "simulated", Epoch: epoch}
	frozen, validFrozen := simulationDecimal(scope.FrozenBalance)
	if scope.SimulationProvenance != expected || !scope.ScopeComplete || !scope.LedgerClosed || scope.Pending != "0" || !validFrozen || frozen.Sign() != 0 || scope.BillingType != "standard_wallet" || len(scope.SourceAccountIDs) != len(b.Sources) {
		return nil
	}
	for _, a := range b.Sources {
		if !slices.Contains(scope.SourceAccountIDs, a.UpstreamID) || a.SourceID != b.SourceID {
			return nil
		}
	}
	network, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	results := make([]allocation.Result, 0, len(b.Sources))
	seenRequests, seenSequences := map[string]bool{}, map[string]bool{}
	for i, a := range b.Sources {
		contribution := &out.Contributions[i]
		result := allocation.Result{State: "evidence_incomplete"}
		var members []json.RawMessage
		if json.Unmarshal([]byte(a.MemberHead), &members) != nil || len(members) > 500 {
			contribution.State = "evidence_limit"
			results = append(results, result)
			continue
		}
		if contribution.State == "evidence_incomplete" && contribution.EstimateState == "scenario" && a.CollectionAvailable && a.SourceMode == "active" {
			var proof *simulationWalletSourceProof
			var state string
			result, proof, state, err = s.simulationWalletAllocation(network, tx, c, b, a, scope, participant, seenRequests, seenSequences)
			if err != nil {
				return err
			}
			if proof != nil {
				contribution.State, contribution.Amount, contribution.Simulation, contribution.SimulationProof = "scenario", result.Members[participant], &expected, proof
				if out.ExpiresAt == nil || proof.ExpiresAt.Before(*out.ExpiresAt) {
					out.ExpiresAt = &proof.ExpiresAt
				}
			} else if state == "allocation_deficit" || state == "condition_changed" || state == "evidence_limit" || state == "contract_unavailable" {
				contribution.State = state
			}
		}
		results = append(results, result)
	}
	after, err := s.walletBasis(ctx, tx, participant)
	if err != nil {
		return err
	}
	fresh := walletRecommendation(after, participant, time.Now().UTC())
	if fresh.BasisHash != out.BasisHash {
		*out = fresh
		return nil
	}
	out.Target = allocation.Global(results, participant)
	if out.Target != nil {
		out.Complete, out.Simulation, out.Global.State = true, &expected, "scenario"
		out.Global.Limitations = []string{"CONDITIONAL_SIMULATION_ONLY", "FINANCIAL_APPLICATION_NOT_IMPLEMENTED"}
	}
	return nil
}
