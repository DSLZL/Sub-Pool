package connection

import (
	"context"

	"time"

	"github.com/jackc/pgx/v5"

	"sub-pool/backend/internal/auth"
)

type WalletAutomationTag struct {
	ControlRevision string `json:"control_revision"`
}

type WalletAutomationBinding struct {
	SourceID           string               `json:"source_id"`
	ConnectionRevision string               `json:"connection_revision"`
	Simulation         SimulationProvenance `json:"simulation"`
}

type walletAutomationAuthority struct {
	Revision, ActorID, ActorRevision string
	Enabled                          bool
	Binding                          *WalletAutomationBinding
	UpdatedAt                        *time.Time
}

func (s *Service) walletApplicationActor(ctx context.Context, tx pgx.Tx, token string, automatic *walletAutomationAuthority) (auth.Identity, error) {
	if automatic != nil {
		return s.auth.AuthorizeAutomationAdmin(ctx, tx, automatic.ActorID, automatic.ActorRevision)
	}
	return s.authorize(ctx, tx, token, true)
}
