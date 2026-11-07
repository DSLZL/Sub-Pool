package connection

import "context"

type WalletAutomation struct {
	Enabled        bool   `json:"enabled"`
	CanEnable      bool   `json:"can_enable"`
	State          string `json:"state"`
	Reason         string `json:"reason"`
	ExecutionState string `json:"execution_state"`
}

func (s *Service) GetWalletAutomation(ctx context.Context, token string) (WalletAutomation, error) {
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return WalletAutomation{}, err
	}
	defer tx.Rollback(ctx)
	if err = s.finish(ctx, tx, token, true); err != nil {
		return WalletAutomation{}, err
	}
	// shortcut: manual financial dispatch is unqualified; add explicit configuration and scheduling only after its acceptance.
	return WalletAutomation{Enabled: false, CanEnable: false, State: "unavailable", Reason: "FINANCIAL_APPLICATION_UNAVAILABLE", ExecutionState: "not_implemented"}, nil
}
