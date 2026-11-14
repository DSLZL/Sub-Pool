package connection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"math/big"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"sub-pool/backend/internal/estimation"
)

type SimulationCostInterval struct {
	StartAt      time.Time         `json:"start_at"`
	EndAt        time.Time         `json:"end_at"`
	StandardCost estimation.Amount `json:"standard_cost"`
	ActualCost   estimation.Amount `json:"actual_cost"`
	EntryCount   int               `json:"entry_count"`
}

// Only aggregates leave the provider; ledger participants and resource credentials stay private.
type SimulationCostProof struct {
	SimulationProvenance
	SourceStart      time.Time                `json:"source_start"`
	ClosedThrough    time.Time                `json:"closed_through"`
	LedgerRevision   string                   `json:"ledger_revision"`
	PriceVersion     string                   `json:"price_version"`
	StandardRate     string                   `json:"standard_rate"`
	WalletMultiplier string                   `json:"wallet_multiplier"`
	BillingType      string                   `json:"billing_type"`
	PriceEffectiveAt time.Time                `json:"price_effective_at"`
	Intervals        []SimulationCostInterval `json:"intervals"`
}

type simulationPrice struct {
	AccountID        string    `json:"account_id"`
	Version          string    `json:"version"`
	StandardRate     string    `json:"standard_rate"`
	WalletMultiplier string    `json:"wallet_multiplier"`
	BillingType      string    `json:"billing_type"`
	EffectiveAt      time.Time `json:"effective_at"`
}

type simulationCostPage struct {
	SimulationProvenance
	AccountID      string            `json:"account_id"`
	ResourceID     string            `json:"resource_id"`
	Start          time.Time         `json:"start"`
	End            time.Time         `json:"end"`
	Interval       string            `json:"interval"`
	SourceStart    time.Time         `json:"source_start"`
	ClosedThrough  time.Time         `json:"closed_through"`
	LedgerRevision string            `json:"ledger_revision"`
	Pending        string            `json:"pending_requests"`
	Complete       bool              `json:"complete"`
	Unit           string            `json:"unit"`
	StandardCost   string            `json:"standard_cost"`
	ActualCost     string            `json:"actual_cost"`
	Prices         []json.RawMessage `json:"prices"`
	Entries        []json.RawMessage `json:"entries"`
	NextAfter      *string           `json:"next_after"`
	BoundExhausted bool              `json:"bound_exhausted"`
}

var simulationMoney = regexp.MustCompile(`^(0|[1-9][0-9]{0,15})(\.[0-9]{1,8})?$`)

func simulationObject(body json.RawMessage, keys string, out any) bool {
	fields, ok := usageObject(body)
	names := strings.Fields(keys)
	if !ok || !json.Valid(body) || len(fields) != len(names) {
		return false
	}
	for _, key := range names {
		value, exists := fields[key]
		if !exists || key != "next_after" && key != "standard_cost" && key != "actual_cost" && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	return d.Decode(out) == nil
}

func simulationTime(value json.RawMessage) bool {
	var text string
	if json.Unmarshal(value, &text) != nil || !observationTimestamp.MatchString(text) {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, text)
	return err == nil && at.Year() >= 1970 && at.Year() < 9999
}

func simulationDecimal(text string) (*big.Rat, bool) {
	if !simulationMoney.MatchString(text) {
		return nil, false
	}
	return estimation.Decimal(text)
}

func (s *Service) simulationCosts(ctx context.Context, c storedConfig, a storedAccount, points []ObservationRecord, latest QuotaSnapshot) (*SimulationCostProof, string) {
	epoch := s.simulationEpoch(c)
	if epoch == "" || a.SourceMode != "active" || len(points) != 3 {
		return nil, "evidence_incomplete"
	}
	expected := SimulationProvenance{Protocol: simulationProtocol, Environment: "simulated", Epoch: epoch}
	for _, snapshot := range append([]QuotaSnapshot{latest}, points[0].Snapshot, points[1].Snapshot, points[2].Snapshot) {
		if snapshot.Simulation == nil || *snapshot.Simulation != expected || snapshot.SourceMode != "active" {
			return nil, "condition_changed"
		}
	}
	kind, secret, err := s.decrypt(c)
	if err != nil {
		return nil, "evidence_incomplete"
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	proof := &SimulationCostProof{SimulationProvenance: expected, Intervals: []SimulationCostInterval{}}
	var price simulationPrice
	requests := make(map[string]bool)
	for i := 0; i < 2; i++ {
		start, end := *points[i].Snapshot.SourceAt, *points[i+1].Snapshot.SourceAt
		query := url.Values{"account_id": {a.UpstreamID}, "start": {start.Format(time.RFC3339Nano)}, "end": {end.Format(time.RFC3339Nano)}, "limit": {"100"}}
		after, count := "0", 0
		standard, actual := new(big.Rat), new(big.Rat)
		var header []byte
		for pageNumber := 0; ; pageNumber++ {
			if pageNumber == 100 {
				return nil, "evidence_limit"
			}
			query.Set("after", after)
			body, check := s.upstream.get(ctx, c.BaseURL, simulationPrefix+"/coverage?"+query.Encode(), kind, secret, "usage.read", true)
			var page simulationCostPage
			if check.State != "available" || !simulationObject(body, "protocol environment epoch account_id resource_id start end interval source_start closed_through ledger_revision pending_requests complete unit standard_cost actual_cost prices entries next_after bound_exhausted", &page) {
				return nil, "evidence_incomplete"
			}
			fields, _ := usageObject(body)
			for _, key := range []string{"start", "end", "source_start", "closed_through"} {
				if !simulationTime(fields[key]) {
					return nil, "evidence_incomplete"
				}
			}
			fingerprint := sha256.Sum256([]byte("sub-pool:openai-resource:" + page.ResourceID))
			if page.SimulationProvenance != expected || page.AccountID != a.UpstreamID || !safeText(page.ResourceID, 1024, secret) || !bytes.Equal(fingerprint[:], a.fingerprint) || !page.Start.Equal(start) || !page.End.Equal(end) {
				return nil, "condition_changed"
			}
			if page.BoundExhausted {
				return nil, "evidence_limit"
			}
			if !page.Complete || page.Pending != "0" || page.Interval != "[start,end)" || page.Unit != "USD" || !validRevision(page.LedgerRevision) || page.SourceStart.After(start) || page.ClosedThrough.Before(end) || page.ClosedThrough.After(time.Now().UTC()) || len(page.Entries) > 100 {
				return nil, "evidence_incomplete"
			}
			claimedStandard, validStandard := simulationDecimal(page.StandardCost)
			claimedActual, validActual := simulationDecimal(page.ActualCost)
			if !validStandard || !validActual || len(page.Prices) > 1 {
				return nil, "condition_changed"
			}
			// Header equality freezes every claim across pages, excluding only the page and cursor.
			delete(fields, "entries")
			delete(fields, "next_after")
			encoded, _ := json.Marshal(fields)
			if pageNumber > 0 && !bytes.Equal(header, encoded) {
				return nil, "condition_changed"
			}
			header = encoded
			if pageNumber == 0 {
				if len(page.Prices) == 0 {
					if len(page.Entries) == 0 && page.NextAfter == nil && claimedStandard.Sign() == 0 && claimedActual.Sign() == 0 {
						return nil, "non_identifying_delta"
					}
					return nil, "evidence_incomplete"
				}
				var current simulationPrice
				if !simulationObject(page.Prices[0], "account_id version standard_rate wallet_multiplier billing_type effective_at", &current) {
					return nil, "evidence_incomplete"
				}
				priceFields, _ := usageObject(page.Prices[0])
				rate, rateOK := simulationDecimal(current.StandardRate)
				_, multiplierOK := simulationDecimal(current.WalletMultiplier)
				if current.AccountID != a.UpstreamID || !validRevision(current.Version) || !rateOK || rate.Sign() <= 0 || !multiplierOK || !simulationTime(priceFields["effective_at"]) || current.BillingType != "standard_wallet" && current.BillingType != "subscription" {
					return nil, "evidence_incomplete"
				}
				if current.EffectiveAt.After(start) {
					return nil, "condition_changed"
				}
				if i == 0 {
					price = current
					proof.SourceStart, proof.ClosedThrough, proof.LedgerRevision = page.SourceStart, page.ClosedThrough, page.LedgerRevision
					proof.PriceVersion, proof.StandardRate, proof.WalletMultiplier, proof.BillingType = price.Version, price.StandardRate, price.WalletMultiplier, price.BillingType
					proof.PriceEffectiveAt = price.EffectiveAt
				} else if price != current || !proof.SourceStart.Equal(page.SourceStart) || !proof.ClosedThrough.Equal(page.ClosedThrough) || proof.LedgerRevision != page.LedgerRevision {
					return nil, "condition_changed"
				}
			}
			for _, raw := range page.Entries {
				var entry struct {
					Seq          string    `json:"seq"`
					Kind         string    `json:"kind"`
					RequestID    string    `json:"request_id"`
					AccountID    string    `json:"account_id"`
					UserID       string    `json:"user_id"`
					ChargedAt    time.Time `json:"charged_at"`
					StandardCost string    `json:"standard_cost"`
					ActualCost   string    `json:"actual_cost"`
					PriceVersion string    `json:"price_version"`
				}
				if !simulationObject(raw, "seq kind request_id account_id user_id charged_at standard_cost actual_cost price_version", &entry) {
					return nil, "evidence_incomplete"
				}
				entryFields, _ := usageObject(raw)
				seq, _ := strconv.ParseInt(entry.Seq, 10, 64)
				previous, _ := strconv.ParseInt(after, 10, 64)
				std, stdOK := simulationDecimal(entry.StandardCost)
				act, actOK := simulationDecimal(entry.ActualCost)
				if !validRevision(entry.Seq) || seq <= previous || entry.Kind != "charge" || !managedID(entry.RequestID) || requests[entry.RequestID] || entry.AccountID != a.UpstreamID || !validRevision(entry.UserID) || entry.PriceVersion != price.Version || !simulationTime(entryFields["charged_at"]) || entry.ChargedAt.Before(start) || !entry.ChargedAt.Before(end) || entry.ChargedAt.Before(price.EffectiveAt) || !stdOK || !actOK {
					return nil, "condition_changed"
				}
				multiplier, _ := simulationDecimal(price.WalletMultiplier)
				expectedActual := new(big.Rat)
				if price.BillingType == "standard_wallet" {
					expectedActual.Mul(std, multiplier)
				}
				if expectedActual.Cmp(act) != 0 {
					return nil, "condition_changed"
				}
				requests[entry.RequestID] = true
				after, count = entry.Seq, count+1
				standard.Add(standard, std)
				actual.Add(actual, act)
			}
			if page.NextAfter != nil {
				if len(page.Entries) != 100 || *page.NextAfter != after {
					return nil, "evidence_incomplete"
				}
				continue
			}
			if standard.Cmp(claimedStandard) != 0 || actual.Cmp(claimedActual) != 0 {
				return nil, "evidence_incomplete"
			}
			if count == 0 {
				return nil, "non_identifying_delta"
			}
			proof.Intervals = append(proof.Intervals, SimulationCostInterval{StartAt: start, EndAt: end, StandardCost: *estimation.Exact(standard), ActualCost: *estimation.Exact(actual), EntryCount: count})
			break
		}
	}
	return proof, ""
}
