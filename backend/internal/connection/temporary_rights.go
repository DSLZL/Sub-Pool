package connection

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
)

type TemporaryRightsChange struct {
	RequestID     string            `json:"request_id"`
	RoundID       string            `json:"round_id"`
	AccountID     string            `json:"account_id"`
	ParticipantID string            `json:"participant_id"`
	BindingID     string            `json:"binding_id"`
	Revision      string            `json:"revision"`
	Period        ObservationPeriod `json:"period"`
	After         string            `json:"after"`
	Reason        string            `json:"reason"`
}
type TemporaryRightsReceipt struct {
	TemporaryRightsChange
	Before     string    `json:"before"`
	Delta      string    `json:"delta"`
	RecordedAt time.Time `json:"recorded_at"`
}
type TemporaryRights struct {
	RoundID     string             `json:"round_id"`
	AccountID   string             `json:"account_id"`
	Revision    string             `json:"revision"`
	Period      *ObservationPeriod `json:"period"`
	Adjustments map[string]string  `json:"adjustments"`
}

func temporarySigned(text string) (*big.Rat, bool) {
	negative := strings.HasPrefix(text, "-")
	if negative {
		text = text[1:]
	}
	value, ok := simulationDecimal(text)
	if !ok || negative && value.Sign() == 0 {
		return nil, false
	}
	if negative {
		value.Neg(value)
	}
	return value, true
}

func temporaryCurrentRights(ctx context.Context, tx pgx.Tx, epoch string, b walletBasis, a walletSource) (map[string]string, error) {
	sums := map[string]*big.Rat{}
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON(round_id) snapshot FROM temporary_events WHERE account_id=$1 AND kind='settlement' ORDER BY round_id,id DESC`, a.AccountID)
	if err != nil {
		return nil, err
	}
	settlements := []TemporarySettlement{}
	for rows.Next() {
		var raw []byte
		var v TemporarySettlement
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &v)
		}
		if err != nil {
			break
		}
		settlements = append(settlements, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, v := range settlements {
		if v.State != "settled" || v.NextPeriod == nil || !time.Now().Before(v.NextPeriod.EndAt) {
			continue
		}
		r, err := temporaryRound(ctx, tx, v.RoundID)
		if err != nil {
			return nil, err
		}
		if r.Scope.SourceID != b.SourceID {
			continue
		}
		var frozen *TemporaryAccount
		for i := range r.Scope.Accounts {
			if r.Scope.Accounts[i].AccountID == a.AccountID {
				frozen = &r.Scope.Accounts[i]
			}
		}
		matching := false
		if a.Estimate != nil {
			for _, w := range a.Estimate.Windows {
				if w.Period != nil && temporarySamePeriod(*w.Period, *v.NextPeriod) {
					matching = true
				}
			}
		}
		if !matching {
			continue
		}
		if r.Scope.Simulation.Epoch != epoch || r.Scope.ConnectionRevision != b.ConnectionRevision || frozen == nil || !temporaryAccountMatches(*frozen, b, a) {
			return nil, auth.ErrConflict
		}
		for id, text := range v.Adjustments {
			value, ok := temporarySigned(text)
			if !ok {
				return nil, auth.ErrConflict
			}
			if sums[id] == nil {
				sums[id] = new(big.Rat)
			}
			sums[id].Add(sums[id], value)
		}
	}
	rows, err = tx.Query(ctx, `SELECT e.snapshot FROM temporary_events e JOIN temporary_rounds r ON r.id=e.round_id WHERE e.account_id=$1 AND e.kind='rights' AND r.snapshot->'scope'->>'source_id'=$2 ORDER BY e.id`, a.AccountID, b.SourceID)
	if err != nil {
		return nil, err
	}
	receipts := []TemporaryRightsReceipt{}
	for rows.Next() {
		var raw []byte
		var v TemporaryRightsReceipt
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &v)
		}
		if err != nil {
			break
		}
		receipts = append(receipts, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, v := range receipts {
		if !time.Now().Before(v.Period.EndAt) {
			continue
		}
		matching := false
		if a.Estimate != nil {
			for _, w := range a.Estimate.Windows {
				if w.Period != nil && temporarySamePeriod(*w.Period, v.Period) {
					matching = true
				}
			}
		}
		if !matching {
			continue
		}
		r, err := temporaryRound(ctx, tx, v.RoundID)
		if err != nil {
			return nil, err
		}
		if r.Scope.Simulation.Epoch != epoch || r.Scope.ConnectionRevision != b.ConnectionRevision {
			return nil, auth.ErrConflict
		}
		var binding string
		if err = tx.QueryRow(ctx, `SELECT id::text FROM participant_bindings WHERE participant_id=$1 AND ended_at IS NULL`, v.ParticipantID).Scan(&binding); err != nil {
			return nil, err
		}
		if binding != v.BindingID {
			return nil, auth.ErrConflict
		}
		value, ok := temporarySigned(v.Delta)
		if !ok {
			return nil, auth.ErrConflict
		}
		if sums[v.ParticipantID] == nil {
			sums[v.ParticipantID] = new(big.Rat)
		}
		sums[v.ParticipantID].Add(sums[v.ParticipantID], value)
	}
	out := map[string]string{}
	for id, value := range sums {
		out[id] = value.FloatString(8)
	}
	return out, rows.Err()
}

func temporaryRightsRevision(ctx context.Context, tx pgx.Tx, account string) (string, error) {
	var revision string
	err := tx.QueryRow(ctx, `SELECT COALESCE(max(id),0)::text FROM temporary_events WHERE account_id=$1`, account).Scan(&revision)
	return revision, err
}

func (s *Service) ChangeTemporaryRights(ctx context.Context, token string, in TemporaryRightsChange) (TemporaryRightsReceipt, error) {
	after, valid := temporarySigned(in.After)
	if !valid || !managedID(in.RequestID) || !managedID(in.RoundID) || !managedID(in.AccountID) || !managedID(in.ParticipantID) || !validRevision(in.BindingID) || !validUsageAfter(in.Revision) || strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 500 {
		return TemporaryRightsReceipt{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return TemporaryRightsReceipt{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, temporaryLock); err != nil {
		return TemporaryRightsReceipt{}, err
	}
	key := "temporary-rights:" + actor.ID + ":" + in.RequestID
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT snapshot FROM temporary_events WHERE request_key=$1 AND kind='rights'`, key).Scan(&raw)
	if err == nil {
		var old TemporaryRightsReceipt
		if err = json.Unmarshal(raw, &old); err != nil {
			return old, err
		}
		if temporaryHash(old.TemporaryRightsChange) != temporaryHash(in) {
			return TemporaryRightsReceipt{}, auth.ErrConflict
		}
		return old, s.finish(ctx, tx, token, true)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return TemporaryRightsReceipt{}, err
	}
	r, err := temporaryRound(ctx, tx, in.RoundID)
	if err != nil {
		return TemporaryRightsReceipt{}, err
	}
	current, err := temporaryLatest(ctx, tx)
	if err != nil {
		return TemporaryRightsReceipt{}, err
	}
	if current != nil {
		exit, err := temporaryEffectiveExit(ctx, tx, *current, time.Now().UTC())
		if err != nil {
			return TemporaryRightsReceipt{}, err
		}
		if exit == nil {
			return TemporaryRightsReceipt{}, auth.ErrConflict
		}
	}
	revision, err := temporaryRightsRevision(ctx, tx, in.AccountID)
	if err != nil {
		return TemporaryRightsReceipt{}, err
	}
	if revision != in.Revision {
		return TemporaryRightsReceipt{}, auth.ErrConflict
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return TemporaryRightsReceipt{}, err
	}
	basis, err := s.walletBasis(ctx, tx, in.ParticipantID)
	if err != nil {
		return TemporaryRightsReceipt{}, err
	}
	p, err := readParticipant(ctx, tx, in.ParticipantID, c, false, false)
	if err != nil {
		return TemporaryRightsReceipt{}, err
	}
	if !p.Enabled || !p.SourceCurrent || p.Binding.ID != in.BindingID || r.Scope.SourceID != basis.SourceID || r.Scope.ConnectionRevision != basis.ConnectionRevision || r.Scope.Simulation.Epoch != s.simulationEpoch(c) || !time.Now().Before(in.Period.EndAt) {
		return TemporaryRightsReceipt{}, auth.ErrConflict
	}
	var source *walletSource
	for i := range basis.Sources {
		if basis.Sources[i].AccountID == in.AccountID {
			source = &basis.Sources[i]
		}
	}
	if source == nil || source.Estimate == nil {
		return TemporaryRightsReceipt{}, auth.ErrConflict
	}
	frozenMember := false
	for _, account := range r.Scope.Accounts {
		if account.AccountID != in.AccountID || !temporaryAccountMatches(account, basis, *source) {
			continue
		}
		for _, member := range account.Members {
			if member.ParticipantID == in.ParticipantID && member.BindingID == in.BindingID {
				frozenMember = true
			}
		}
	}
	if !frozenMember {
		return TemporaryRightsReceipt{}, auth.ErrConflict
	}
	matching := false
	for _, w := range source.Estimate.Windows {
		if w.Period != nil && temporarySamePeriod(*w.Period, in.Period) {
			matching = true
		}
	}
	if !matching {
		return TemporaryRightsReceipt{}, auth.ErrConflict
	}
	baseline := walletRecommendation(basis, in.ParticipantID, time.Now().UTC())
	qualified := false
	for _, row := range baseline.Contributions {
		if row.AccountID == in.AccountID && row.State == "evidence_incomplete" && row.EstimateState == "scenario" {
			qualified = true
		}
	}
	if !qualified {
		return TemporaryRightsReceipt{}, auth.ErrConflict
	}
	rights, err := temporaryCurrentRights(ctx, tx, s.simulationEpoch(c), basis, *source)
	if err != nil {
		return TemporaryRightsReceipt{}, err
	}
	before := new(big.Rat)
	if text := rights[in.ParticipantID]; text != "" {
		var ok bool
		before, ok = temporarySigned(text)
		if !ok {
			return TemporaryRightsReceipt{}, auth.ErrConflict
		}
	}
	var read WalletRead
	network, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	s.readSimulationWallet(network, c, p, &read)
	if read.SimulationScope == nil || read.Read.State != "available" || read.FieldAuthority.State != "available" || !simulationWalletClosed(*read.SimulationScope) || len(read.SimulationScope.SourceAccountIDs) != len(basis.Sources) {
		return TemporaryRightsReceipt{}, auth.ErrConflict
	}
	for _, a := range basis.Sources {
		found := false
		for _, id := range read.SimulationScope.SourceAccountIDs {
			if id == a.UpstreamID {
				found = true
			}
		}
		if !found {
			return TemporaryRightsReceipt{}, auth.ErrConflict
		}
	}
	result, proof, _, err := s.simulationWalletAllocationWithRights(network, tx, c, basis, *source, read.SimulationScope, in.ParticipantID, map[string]bool{}, map[string]bool{}, &in)
	if err != nil {
		return TemporaryRightsReceipt{}, err
	}
	if result.State != "scenario" || proof == nil {
		return TemporaryRightsReceipt{}, auth.ErrConflict
	}
	fresh, err := s.walletBasis(ctx, tx, in.ParticipantID)
	if err != nil {
		return TemporaryRightsReceipt{}, err
	}
	if walletRecommendation(fresh, in.ParticipantID, time.Now().UTC()).BasisHash != baseline.BasisHash || !time.Now().Before(proof.ExpiresAt) {
		return TemporaryRightsReceipt{}, auth.ErrConflict
	}
	out := TemporaryRightsReceipt{TemporaryRightsChange: in, Before: before.FloatString(8), Delta: new(big.Rat).Sub(after, before).FloatString(8), RecordedAt: time.Now().UTC().Truncate(time.Microsecond)}
	raw, err = json.Marshal(out)
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO temporary_events(round_id,account_id,kind,request_key,recorded_at,snapshot) VALUES($1,$2,'rights',$3,$4,$5)`, in.RoundID, in.AccountID, key, out.RecordedAt, raw); err != nil {
		return TemporaryRightsReceipt{}, err
	}
	_, err = audit.Record(ctx, tx, audit.Entry{ActorID: &actor.ID, Action: "temporary.rights", Outcome: "succeeded", TargetType: "participant", TargetID: in.ParticipantID})
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return TemporaryRightsReceipt{}, err
	}
	return out, nil
}
