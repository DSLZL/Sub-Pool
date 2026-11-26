package connection

import (
	"time"

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
	personalEffect *WalletTemporaryEffect
}
