package connection

import (
	"time"
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
