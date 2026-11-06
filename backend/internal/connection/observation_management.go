package connection

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
)

var ErrObservationRange = &auth.Failure{Status: 409, Code: "OBSERVATION_RANGE_UNVERIFIED"}

type ObservationPeriod struct {
	Kind            string    `json:"kind"`
	StartAt         time.Time `json:"start_at"`
	EndAt           time.Time `json:"end_at"`
	DurationSeconds string    `json:"duration_seconds"`
	Basis           string    `json:"basis"`
}

type ObservationStart struct {
	Event  ObservationEvent   `json:"event"`
	Start  ObservationRecord  `json:"start"`
	End    ObservationRecord  `json:"end"`
	Period *ObservationPeriod `json:"period"`
	Usable bool               `json:"usable"`
	Reason string             `json:"reason"`
}

type ObservationManagement struct {
	AccountID          string            `json:"account_id"`
	Revision           string            `json:"revision"`
	ManualStart        *ObservationStart `json:"manual_start"`
	RecalculationState string            `json:"recalculation_state"`
}

func observationPeriod(first, last ObservationRecord, kind string) *ObservationPeriod {
	a, b := first.Snapshot, last.Snapshot
	if first.ID == last.ID || first.AccountID != last.AccountID || a.SourceID != b.SourceID || a.SourceMode != b.SourceMode || a.SourceAt == nil || b.SourceAt == nil || !b.SourceAt.After(*a.SourceAt) || a.SourceAt.After(a.ReceivedAt) || b.SourceAt.After(b.ReceivedAt) {
		return nil
	}
	var left, right *QuotaWindow
	for i := range a.Windows {
		if a.Windows[i].Kind == kind {
			left = &a.Windows[i]
		}
	}
	for i := range b.Windows {
		if b.Windows[i].Kind == kind {
			right = &b.Windows[i]
		}
	}
	if left == nil || right == nil || left.ResetAt == nil || right.ResetAt == nil || left.DurationSeconds == nil || right.DurationSeconds == nil || left.UsedPercent == nil || right.UsedPercent == nil || !left.ResetAt.Equal(*right.ResetAt) || *left.DurationSeconds != *right.DurationSeconds {
		return nil
	}
	seconds, err := strconv.ParseInt(*left.DurationSeconds, 10, 64)
	if err != nil || seconds <= 0 || seconds > 315360000 {
		return nil
	}
	start := left.ResetAt.Add(-time.Duration(seconds) * time.Second)
	if a.SourceAt.Before(start) || !b.SourceAt.Before(*left.ResetAt) {
		return nil
	}
	return &ObservationPeriod{Kind: kind, StartAt: start, EndAt: *left.ResetAt, DurationSeconds: *left.DurationSeconds, Basis: "native_reset_and_duration"}
}

func observationStartReason(first, last ObservationRecord, period *ObservationPeriod, a storedAccount, c storedConfig, policy MonitoringConfig, now time.Time) string {
	if period == nil {
		return "PERIOD_EVIDENCE_MISSING"
	}
	if first.Excluded || last.Excluded {
		return "ENDPOINT_EXCLUDED"
	}
	if !a.SourceCurrent || first.Snapshot.SourceID != a.SourceID || last.Snapshot.SourceID != a.SourceID {
		return "SOURCE_CHANGED"
	}
	if !a.ConnectionAvailable {
		return "CONNECTION_UNAVAILABLE"
	}
	if first.Snapshot.ConnectionRevision != c.Revision || last.Snapshot.ConnectionRevision != c.Revision {
		return "CONNECTION_CHANGED"
	}
	if a.IdentityState != "verified" {
		return "IDENTITY_UNVERIFIED"
	}
	if first.Snapshot.SourceMode != a.SourceMode || last.Snapshot.SourceMode != a.SourceMode {
		return "SOURCE_MODE_CHANGED"
	}
	if last.Snapshot.SourceAt == nil || last.Snapshot.SourceAt.After(now) {
		return "SOURCE_TIME_UNVERIFIED"
	}
	if !now.Before(period.EndAt) {
		return "WINDOW_EXPIRED"
	}
	if now.Sub(*last.Snapshot.SourceAt) > time.Duration(policy.StaleAfterSeconds)*time.Second {
		return "SOURCE_STALE"
	}
	return "VERIFIED_LOCAL_RANGE"
}

func readObservationManagement(ctx context.Context, tx pgx.Tx, a storedAccount, c storedConfig, policy MonitoringConfig, admin bool) (ObservationManagement, error) {
	state := ObservationManagement{AccountID: a.ID, Revision: "1", RecalculationState: "not_implemented"}
	err := tx.QueryRow(ctx, `SELECT revision::text FROM observation_management WHERE account_id=$1`, a.ID).Scan(&state.Revision)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return state, err
	}
	event, err := scanObservationEvent(tx.QueryRow(ctx, `SELECT `+observationEventColumns+` FROM observation_changes WHERE account_id=$1 AND action IN ('start','revoke') ORDER BY observation_changes.id DESC LIMIT 1`, a.ID), admin)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && event.Action == "revoke" {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if event.StartObservationID == nil || event.EndObservationID == nil || event.WindowKind == nil {
		return state, auth.ErrConflict
	}
	first, err := readObservation(ctx, tx, *event.StartObservationID, a.ID, auth.Access{AllData: true})
	if err != nil {
		return state, err
	}
	last, err := readObservation(ctx, tx, *event.EndObservationID, a.ID, auth.Access{AllData: true})
	if err != nil {
		return state, err
	}
	period := observationPeriod(first, last, *event.WindowKind)
	reason := observationStartReason(first, last, period, a, c, policy, time.Now().UTC())
	state.ManualStart = &ObservationStart{Event: event, Start: first, End: last, Period: period, Usable: reason == "VERIFIED_LOCAL_RANGE", Reason: reason}
	return state, nil
}

func (s *Service) GetObservationManagement(ctx context.Context, token, account string) (ObservationManagement, error) {
	if !managedID(account) {
		return ObservationManagement{}, auth.ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ObservationManagement{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "observations")
	if err == nil {
		err = access.CheckAccount(account)
	}
	if err != nil {
		return ObservationManagement{}, err
	}
	policy, err := readMonitoringConfig(ctx, tx, false)
	if err != nil {
		return ObservationManagement{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return ObservationManagement{}, err
	}
	a, err := readManaged(ctx, tx, account, c, false)
	if err != nil {
		return ObservationManagement{}, err
	}
	state, err := readObservationManagement(ctx, tx, a, c, policy, access.AllData)
	if err != nil {
		return ObservationManagement{}, err
	}
	if err = s.finishObservationRead(ctx, tx, token); err != nil {
		return ObservationManagement{}, err
	}
	return state, nil
}

func lockObservationManagement(ctx context.Context, tx pgx.Tx, account, revision string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO observation_management(account_id) VALUES ($1) ON CONFLICT DO NOTHING`, account); err != nil {
		return err
	}
	var current string
	if err := tx.QueryRow(ctx, `SELECT revision::text FROM observation_management WHERE account_id=$1 FOR UPDATE`, account).Scan(&current); err != nil {
		return err
	}
	if current != revision {
		return auth.ErrConflict
	}
	return nil
}

func appendObservationChange(ctx context.Context, tx pgx.Tx, a storedAccount, actor auth.Identity, event ObservationEvent) error {
	if err := tx.QueryRow(ctx, `UPDATE observation_management SET revision=revision+1 WHERE account_id=$1 RETURNING revision::text`, a.ID).Scan(&event.Revision); err != nil {
		return err
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	_, err := tx.Exec(ctx, `INSERT INTO observation_changes(account_id,revision,action,observation_id,start_observation_id,end_observation_id,window_kind,reason,actor_id,occurred_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, a.ID, event.Revision, event.Action, event.ObservationID, event.StartObservationID, event.EndObservationID, event.WindowKind, event.Reason, actor.ID, at)
	if err == nil {
		_, err = audit.Record(ctx, tx, audit.Entry{ActorID: &actor.ID, Action: "observation." + event.Action, Outcome: "succeeded", TargetType: "managed_account", TargetID: a.ID, Revision: &event.Revision, SourceID: &a.SourceID})
	}
	return err
}

type ObservationReviewInput struct {
	AccountID     string `json:"account_id"`
	Revision      string `json:"revision"`
	ObservationID string `json:"observation_id"`
	Excluded      bool   `json:"excluded"`
	Reason        string `json:"reason"`
}

func (s *Service) ReviewObservation(ctx context.Context, token string, input ObservationReviewInput) (ObservationRecord, error) {
	if !managedID(input.AccountID) || !validRevision(input.Revision) || !validRevision(input.ObservationID) || !observationReason(input.Reason) {
		return ObservationRecord{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return ObservationRecord{}, err
	}
	defer tx.Rollback(ctx)
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return ObservationRecord{}, err
	}
	a, err := readManaged(ctx, tx, input.AccountID, c, true)
	if err != nil {
		return ObservationRecord{}, err
	}
	if err = lockObservationManagement(ctx, tx, a.ID, input.Revision); err != nil {
		return ObservationRecord{}, err
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return ObservationRecord{}, err
	}
	item, err := readObservation(ctx, tx, input.ObservationID, a.ID, auth.Access{AllData: true})
	if err != nil {
		return ObservationRecord{}, err
	}
	if item.Excluded == input.Excluded {
		return ObservationRecord{}, auth.ErrConflict
	}
	action := "restore"
	if input.Excluded {
		action = "exclude"
	}
	if err = appendObservationChange(ctx, tx, a, actor, ObservationEvent{Action: action, ObservationID: &input.ObservationID, Reason: input.Reason}); err != nil {
		return ObservationRecord{}, err
	}
	result, err := readObservation(ctx, tx, item.ID, a.ID, auth.Access{AllData: true})
	if err != nil {
		return ObservationRecord{}, err
	}
	if err = s.finish(ctx, tx, token, true); err != nil {
		return ObservationRecord{}, err
	}
	return result, nil
}

type ObservationStartInput struct {
	AccountID          string `json:"account_id"`
	Revision           string `json:"revision"`
	StartObservationID string `json:"start_observation_id"`
	EndObservationID   string `json:"end_observation_id"`
	WindowKind         string `json:"window_kind"`
	Reason             string `json:"reason"`
}

func (s *Service) ChangeObservationStart(ctx context.Context, token string, input ObservationStartInput, revoke bool) (ObservationManagement, error) {
	valid := managedID(input.AccountID) && validRevision(input.Revision) && observationReason(input.Reason)
	if revoke {
		valid = valid && input.StartObservationID == "" && input.EndObservationID == "" && input.WindowKind == ""
	} else {
		valid = valid && validRevision(input.StartObservationID) && validRevision(input.EndObservationID) && input.StartObservationID != input.EndObservationID && (input.WindowKind == "short" || input.WindowKind == "seven_day")
	}
	if !valid {
		return ObservationManagement{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return ObservationManagement{}, err
	}
	defer tx.Rollback(ctx)
	policy, err := readMonitoringConfig(ctx, tx, false)
	if err != nil {
		return ObservationManagement{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return ObservationManagement{}, err
	}
	a, err := readManaged(ctx, tx, input.AccountID, c, true)
	if err != nil {
		return ObservationManagement{}, err
	}
	if err = lockObservationManagement(ctx, tx, a.ID, input.Revision); err != nil {
		return ObservationManagement{}, err
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return ObservationManagement{}, err
	}
	event := ObservationEvent{Action: "revoke", Reason: input.Reason}
	if revoke {
		state, e := readObservationManagement(ctx, tx, a, c, policy, true)
		if e != nil {
			return ObservationManagement{}, e
		}
		if state.ManualStart == nil {
			return ObservationManagement{}, auth.ErrConflict
		}
	} else {
		first, e := readObservation(ctx, tx, input.StartObservationID, a.ID, auth.Access{AllData: true})
		if e != nil {
			return ObservationManagement{}, e
		}
		last, e := readObservation(ctx, tx, input.EndObservationID, a.ID, auth.Access{AllData: true})
		if e != nil {
			return ObservationManagement{}, e
		}
		period := observationPeriod(first, last, input.WindowKind)
		if observationStartReason(first, last, period, a, c, policy, time.Now().UTC()) != "VERIFIED_LOCAL_RANGE" {
			return ObservationManagement{}, ErrObservationRange
		}
		event.Action = "start"
		event.StartObservationID = &input.StartObservationID
		event.EndObservationID = &input.EndObservationID
		event.WindowKind = &input.WindowKind
	}
	if err = appendObservationChange(ctx, tx, a, actor, event); err != nil {
		return ObservationManagement{}, err
	}
	result, err := readObservationManagement(ctx, tx, a, c, policy, true)
	if err != nil {
		return ObservationManagement{}, err
	}
	if err = s.finish(ctx, tx, token, true); err != nil {
		return ObservationManagement{}, err
	}
	return result, nil
}
