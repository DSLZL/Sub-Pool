package connection

import "context"

type TemporaryStatus struct {
	CanStart           bool   `json:"can_start"`
	State              string `json:"state"`
	Reason             string `json:"reason"`
	RoundState         string `json:"round_state"`
	RightsState        string `json:"rights_state"`
	RemindersAvailable bool   `json:"reminders_available"`
}

func (s *Service) GetTemporaryStatus(ctx context.Context, token string) (TemporaryStatus, error) {
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return TemporaryStatus{}, err
	}
	defer tx.Rollback(ctx)
	if err = s.finish(ctx, tx, token, true); err != nil {
		return TemporaryStatus{}, err
	}
	// shortcut: financial dispatch and safe withdrawal are unqualified; add rounds and rights only after their acceptance.
	return TemporaryStatus{State: "unavailable", Reason: "FINANCIAL_APPLICATION_UNAVAILABLE", RoundState: "not_implemented", RightsState: "not_implemented"}, nil
}
