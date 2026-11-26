package connection

import (
	"math/big"

	"regexp"

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

var simulationMoney = regexp.MustCompile(`^(0|[1-9][0-9]{0,15})(\.[0-9]{1,8})?$`)

func simulationDecimal(text string) (*big.Rat, bool) {
	if !simulationMoney.MatchString(text) {
		return nil, false
	}
	return estimation.Decimal(text)
}
