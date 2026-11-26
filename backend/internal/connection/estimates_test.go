package connection

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sub-pool/backend/internal/auth"
	"sync/atomic"
	"testing"
	"time"
)

func estimateFixture(t *testing.T) (*Service, *pgxpool.Pool, string, Configuration, *atomic.Int32) {
	t.Helper()
	s, db, token := setup(t)
	calls := new(atomic.Int32)
	quota := new(atomic.Int32)
	reset := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.Header.Get("x-api-key") != "adm_sc_test_only_secret" || r.Header.Get("Cookie") != "" {
			t.Error("unsafe estimate fixture request")
		}
		if r.URL.Path == "/api/v1/admin/usage" {
			respond(w, map[string]any{"items": []any{}, "total": 0, "page": 1, "page_size": 100})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/admin/accounts/") {
			id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/accounts/"))
			row := fullManagedAccount(id, fmt.Sprintf("estimate-resource-%d", id))
			row["extra"] = map[string]any{"codex_usage_updated_at": time.Now().UTC().Format(time.RFC3339Nano), "codex_5h_used_percent": quota.Add(1), "codex_5h_window_minutes": 180, "codex_5h_reset_at": reset.Format(time.RFC3339), "codex_7d_used_percent": 42}
			respond(w, row)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	return s, db, token, save(t, s, token, server.URL), calls
}

func TestNativePeriodRejectsSubmicrosecondFutureSourceWithoutTolerance(t *testing.T) {
	source := time.Date(2026, 11, 7, 1, 0, 0, 123000, time.UTC)
	reset := source.Add(time.Hour)
	duration, percent := "10800", "2e1"
	snapshot := QuotaSnapshot{SourceAt: &source, ReceivedAt: source.Add(-200 * time.Nanosecond), Windows: []QuotaWindow{{Kind: "short", DurationSeconds: &duration, UsedPercent: &percent, ResetAt: &reset}}}
	if nativeObservationPeriod(snapshot, "short") != nil {
		t.Fatal("future source was relabeled as a verified official period")
	}
	snapshot.ReceivedAt = source
	if period := nativeObservationPeriod(snapshot, "short"); period == nil || period.DurationSeconds != duration {
		t.Fatal("verified native timestamp lost its actual duration", period)
	}
}

func TestEstimateScopedReadersCurrentGrantsAndLocalOnlyHistory(t *testing.T) {
	s, db, token, c, calls := estimateFixture(t)
	ctx := context.Background()
	a := passiveManaged(t, s, token, c, 1)
	b := passiveManaged(t, s, token, c, 2)
	one, err := seedArchivedEstimate(ctx, db, a, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	two, err := seedArchivedEstimate(ctx, db, b, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.auth.CreateUser(ctx, token, auth.UserCreate{Username: "estimate.reader", Password: "ReaderPassword123", Enabled: true, Grants: auth.Grants{Features: []string{"estimates"}, AccountIDs: []string{a.ID}, ParticipantIDs: []string{}}})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := s.auth.Login(ctx, user.Username, "ReaderPassword123", netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	if r, err := s.GetEstimateRecord(ctx, reader, one.ID); err != nil || r.AccountID != a.ID {
		t.Fatal("estimate-only reader required unrelated feature", r, err)
	}
	for _, id := range []string{two.ID, "9223372036854775807"} {
		if _, err = s.GetEstimateRecord(ctx, reader, id); !errors.Is(err, auth.ErrNotFound) {
			t.Fatal("hidden record revealed", err)
		}
	}
	if _, err = s.GetEstimateCurrent(ctx, reader, b.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("hidden account current revealed", err)
	}
	history, err := s.ListEstimateHistory(ctx, reader, EstimateHistoryInput{AccountID: a.ID, After: "0", Limit: 100, WindowKind: "all"})
	if err != nil || len(history.Records) != 1 {
		t.Fatal(history, err)
	}
	if _, err = s.auth.UpdateUser(ctx, token, auth.UserUpdate{ID: user.ID, Revision: user.Revision, Username: user.Username, Enabled: true, Grants: auth.Grants{Features: []string{"estimates"}, AccountIDs: []string{}, ParticipantIDs: []string{}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetEstimateRecord(ctx, reader, one.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("scope contraction leaked old result", err)
	}
	if calls.Load() != before {
		t.Fatal("history or denied operation contacted upstream")
	}
}
