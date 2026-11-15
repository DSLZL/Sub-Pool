package connection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"reflect"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/estimation"
)

const temporaryLock int64 = 0x5350324154454d50

type TemporaryMember struct {
	ParticipantID string             `json:"participant_id"`
	BindingID     string             `json:"binding_id"`
	UserID        string             `json:"user_id"`
	Baseline      *estimation.Amount `json:"baseline"`
}
type TemporaryAccount struct {
	AccountID          string              `json:"account_id"`
	UpstreamID         string              `json:"upstream_id"`
	AccountRevision    string              `json:"account_revision"`
	ContractID         string              `json:"contract_id"`
	ContractHead       string              `json:"contract_head"`
	MemberHead         string              `json:"member_head"`
	EstimateRevision   string              `json:"estimate_revision"`
	ManagementRevision string              `json:"management_revision"`
	PriceVersion       string              `json:"price_version"`
	WalletMultiplier   string              `json:"wallet_multiplier"`
	Periods            []ObservationPeriod `json:"periods"`
	SettlementPeriod   ObservationPeriod   `json:"settlement_period"`
	Remaining          *estimation.Amount  `json:"remaining"`
	Reserve            *estimation.Amount  `json:"reserve"`
	Members            []TemporaryMember   `json:"members"`
}
type TemporaryWallet struct {
	WalletExpected
	UserID           string `json:"user_id"`
	RecommendationID string `json:"recommendation_id"`
	BasisHash        string `json:"basis_hash"`
}
type TemporaryScope struct {
	Simulation         SimulationProvenance `json:"simulation"`
	SourceID           string               `json:"source_id"`
	ConnectionRevision string               `json:"connection_revision"`
	PolicyRevision     string               `json:"policy_revision"`
	ExpiresAt          time.Time            `json:"expires_at"`
	LatestDeadline     time.Time            `json:"latest_deadline"`
	Accounts           []TemporaryAccount   `json:"accounts"`
	Wallets            []TemporaryWallet    `json:"wallets"`
}
type TemporaryPreflight struct {
	State     string          `json:"state"`
	Reason    string          `json:"reason"`
	ScopeHash string          `json:"scope_hash"`
	Scope     *TemporaryScope `json:"scope"`
}

func temporaryHash(value any) string {
	body, _ := json.Marshal(value)
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:])
}

func temporaryParticipants(ctx context.Context, tx pgx.Tx, source string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT cp.participant_id FROM pool_contract_participants cp
 JOIN pool_contract_accounts ca ON ca.contract_id=cp.contract_id AND ca.current
 JOIN pool_contracts pc ON pc.id=ca.contract_id
 WHERE pc.enabled AND pc.source_id=$1 ORDER BY cp.participant_id LIMIT 501`, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Service) GetTemporaryPreflight(ctx context.Context, token string) (TemporaryPreflight, error) {
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return TemporaryPreflight{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, temporaryLock); err != nil {
		return TemporaryPreflight{}, err
	}
	out, err := s.temporaryPreflight(ctx, tx)
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return TemporaryPreflight{}, err
	}
	return out, nil
}

func (s *Service) temporaryPreflight(ctx context.Context, tx pgx.Tx) (TemporaryPreflight, error) {
	out := TemporaryPreflight{State: "unavailable", Reason: "FINANCIAL_APPLICATION_UNAVAILABLE"}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return out, err
	}
	epoch := s.simulationEpoch(c)
	if epoch == "" || c.SourceID == nil {
		return out, nil
	}
	out.Reason = "TEMPORARY_EVIDENCE_INCOMPLETE"
	now := time.Now().UTC()
	ids, err := temporaryParticipants(ctx, tx, *c.SourceID)
	if err != nil {
		return out, err
	}
	if len(ids) == 0 || len(ids) > 500 {
		return out, nil
	}
	scope := TemporaryScope{Simulation: SimulationProvenance{Protocol: simulationProtocol, Environment: "simulated", Epoch: epoch}, SourceID: *c.SourceID, ConnectionRevision: c.Revision, Accounts: []TemporaryAccount{}, Wallets: []TemporaryWallet{}}
	accounts := map[string]*TemporaryAccount{}
	bases := map[string]string{}
	network, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	for _, id := range ids {
		p, err := readParticipant(ctx, tx, id, c, false, false)
		if err != nil {
			return out, err
		}
		basis, err := s.walletBasis(ctx, tx, id)
		if err != nil {
			return out, err
		}
		if !p.Enabled || !p.SourceCurrent || len(basis.Sources) == 0 || len(basis.Sources) > 500 {
			return out, nil
		}
		read, err := latestWalletRead(ctx, tx, id, p.Binding.ID, false)
		if err != nil {
			return out, err
		}
		rec, err := latestWalletRecommendation(ctx, tx, id, p.Binding.ID)
		if err != nil {
			return out, err
		}
		current := s.simulationWalletCurrent(c, basis, id, now, rec)
		if read == nil || read.SimulationScope == nil || rec == nil || !current.Complete || current.Target == nil || current.ExpiresAt == nil {
			return out, nil
		}
		_, _, _, reason := simulationWalletTarget(current.Target, simulationWalletQuantum)
		if reason != "" {
			out.Reason = reason
			return out, nil
		}
		var unresolved bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_application_requests r WHERE r.participant_id=$1 AND NOT (`+walletApplicationResolvedSQL+`))`, id).Scan(&unresolved); err != nil {
			return out, err
		}
		if unresolved {
			out.Reason = "SIMULATION_UNRESOLVED_APPLICATION"
			return out, nil
		}
		var fresh WalletRead
		s.readSimulationWallet(network, c, p, &fresh)
		old := read.SimulationScope
		if fresh.Read.State != "available" || fresh.FieldAuthority.State != "available" || fresh.SimulationScope == nil || !simulationWalletClosed(*fresh.SimulationScope) {
			return out, nil
		}
		actual := fresh.SimulationScope
		if actual.SimulationProvenance != scope.Simulation || actual.UserID != old.UserID || actual.WalletVersion != old.WalletVersion || actual.LedgerRevision != old.LedgerRevision || !simulationMoneyEqual(actual.Balance, old.Balance) || actual.ClosedThrough.Before(old.ClosedThrough) || !simulationWalletAccounts(actual.SourceAccountIDs, old.SourceAccountIDs) {
			out.Reason = "TEMPORARY_WALLET_CHANGED"
			return out, nil
		}
		available, err := s.simulationWalletWriteAvailable(network, c, p.Binding.UpstreamUserID)
		if err != nil {
			return out, err
		}
		if !available {
			out.Reason = "SIMULATION_WRITE_UNAVAILABLE"
			return out, nil
		}
		if scope.PolicyRevision == "" {
			scope.PolicyRevision = basis.PolicyRevision
		} else if scope.PolicyRevision != basis.PolicyRevision {
			return out, nil
		}
		if scope.ExpiresAt.IsZero() || current.ExpiresAt.Before(scope.ExpiresAt) {
			scope.ExpiresAt = *current.ExpiresAt
		}
		bases[id] = current.BasisHash
		scope.Wallets = append(scope.Wallets, TemporaryWallet{WalletExpected: WalletExpected{ParticipantID: id, ParticipantRevision: p.Revision, BindingID: p.Binding.ID, ConnectionRevision: c.Revision, SourceID: *c.SourceID, LatestID: read.ID}, UserID: p.Binding.UpstreamUserID, RecommendationID: rec.ID, BasisHash: current.BasisHash})
		for i, a := range basis.Sources {
			if i >= len(current.Contributions) || a.Contract == nil || a.Estimate == nil {
				return out, nil
			}
			row := current.Contributions[i]
			proof := row.SimulationProof
			if row.AccountID != a.AccountID || row.State != "scenario" || row.Amount == nil || proof == nil || proof.Remaining == nil || proof.Reserve == nil {
				return out, nil
			}
			account := accounts[a.AccountID]
			candidate := TemporaryAccount{AccountID: a.AccountID, UpstreamID: a.UpstreamID, AccountRevision: a.Revision, ContractID: a.Contract.ID, ContractHead: a.ContractHead, MemberHead: a.MemberHead, EstimateRevision: a.EstimateRevision, ManagementRevision: a.ManagementRevision, Periods: []ObservationPeriod{}, Remaining: proof.Remaining, Reserve: proof.Reserve, Members: []TemporaryMember{}}
			for _, window := range a.Estimate.Windows {
				if window.Period == nil || window.CostProof == nil {
					return out, nil
				}
				candidate.Periods = append(candidate.Periods, *window.Period)
				value, vOK := window.Remaining.Rational()
				minimum, mOK := a.Estimate.Remaining.Rational()
				if !vOK || !mOK {
					return out, nil
				}
				if value.Cmp(minimum) == 0 && (candidate.SettlementPeriod.EndAt.IsZero() || window.Period.EndAt.Before(candidate.SettlementPeriod.EndAt) || window.Period.EndAt.Equal(candidate.SettlementPeriod.EndAt) && window.Kind < candidate.SettlementPeriod.Kind) {
					candidate.SettlementPeriod = *window.Period
				}
				candidate.PriceVersion = window.CostProof.PriceVersion
				candidate.WalletMultiplier = window.CostProof.WalletMultiplier
				if scope.LatestDeadline.IsZero() || window.Period.EndAt.Before(scope.LatestDeadline) {
					scope.LatestDeadline = window.Period.EndAt
				}
			}
			sort.Slice(candidate.Periods, func(i, j int) bool { return candidate.Periods[i].Kind < candidate.Periods[j].Kind })
			if account == nil {
				account = &candidate
				accounts[a.AccountID] = account
			} else {
				frozen := *account
				frozen.Members = []TemporaryMember{}
				if !reflect.DeepEqual(frozen, candidate) {
					return out, nil
				}
			}
			account.Members = append(account.Members, TemporaryMember{ParticipantID: id, BindingID: p.Binding.ID, UserID: p.Binding.UpstreamUserID, Baseline: row.Amount})
		}
	}
	keys := make([]string, 0, len(accounts))
	for id := range accounts {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for _, id := range keys {
		a := accounts[id]
		total, ok := a.Reserve.Rational()
		remaining, rOK := a.Remaining.Rational()
		if !ok || !rOK || len(a.Members) == 0 {
			return out, nil
		}
		total = new(big.Rat).Set(total)
		for _, m := range a.Members {
			value, ok := m.Baseline.Rational()
			if !ok {
				return out, nil
			}
			total.Add(total, value)
		}
		if total.Cmp(remaining) != 0 {
			return out, nil
		}
		scope.Accounts = append(scope.Accounts, *a)
	}
	latestIDs, err := temporaryParticipants(ctx, tx, *c.SourceID)
	if err != nil {
		return out, err
	}
	if !reflect.DeepEqual(ids, latestIDs) {
		return out, nil
	}
	for _, id := range ids {
		basis, err := s.walletBasis(ctx, tx, id)
		if err != nil {
			return out, err
		}
		if walletRecommendation(basis, id, time.Now().UTC()).BasisHash != bases[id] {
			out.Reason = "TEMPORARY_INPUT_CHANGED"
			return out, nil
		}
	}
	if !time.Now().Before(scope.ExpiresAt) || !now.Before(scope.LatestDeadline) {
		return out, nil
	}
	out.State, out.Reason, out.ScopeHash, out.Scope = "ready", "CONDITIONAL_SIMULATION_ONLY", temporaryHash(scope), &scope
	return out, nil
}
