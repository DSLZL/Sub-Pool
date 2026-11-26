package connection

import (
	"time"

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
