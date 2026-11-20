package telemetry

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestRequestsCorrelateAndRedactErrors(t *testing.T) {
	var logs bytes.Buffer
	logger, err := NewLogger(&logs, "info")
	if err != nil {
		t.Fatal(err)
	}
	previous := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(previous) })
	secrets := []string{"test-password", "test-session-cookie", "test-machine-key", "test-upstream-token", "test-smtp-password", "test-db-dsn", "test-header-id", "test-path-id"}
	mux := http.NewServeMux()
	mux.HandleFunc("/accounts/{id}", func(w http.ResponseWriter, r *http.Request) {
		SetActor(r.Context(), "confirmed-user")
		Error(r.Context(), "lookup_account", &pgconn.PgError{Code: "42P01", Message: strings.Join(secrets, " ")})
		w.WriteHeader(http.StatusInternalServerError)
	})
	request := httptest.NewRequest(http.MethodPost, "/accounts/"+secrets[7]+"?password="+secrets[0], nil)
	request.Header.Set("Cookie", secrets[1])
	request.Header.Set("Authorization", secrets[2])
	request.Header.Set(RequestIDHeader, secrets[6])
	response := httptest.NewRecorder()
	Requests(mux, map[string]struct{}{"/accounts/{id}": {}}, mux).ServeHTTP(response, request)
	id := response.Header().Get(RequestIDHeader)
	if response.Code != 500 || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) {
		t.Fatal("missing generated request ID or incorrect status")
	}
	for _, secret := range secrets {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("diagnostic output contained a secret")
		}
	}
	read := json.NewDecoder(&logs)
	var failure, requestLog map[string]any
	if err := read.Decode(&failure); err != nil {
		t.Fatal(err)
	}
	if err := read.Decode(&requestLog); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []map[string]any{failure, requestLog} {
		if entry["request_id"] != id || entry["user_id"] != "confirmed-user" {
			t.Fatal("log entry could not be joined to the authenticated request")
		}
	}
	if failure["sqlstate"] != "42P01" || failure["operation"] != "lookup_account" || requestLog["route"] != "/accounts/{id}" ||
		requestLog["method"] != "POST" || requestLog["status"] != float64(500) || requestLog["duration_ms"] == nil {
		t.Fatal("missing structured failure or request diagnostic")
	}
}

func TestInvalidLevelAndStaleHeartbeat(t *testing.T) {
	var out bytes.Buffer
	logger, err := NewLogger(&out, "warn")
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("hidden")
	logger.Warn("visible")
	if strings.Contains(out.String(), "hidden") || !strings.Contains(out.String(), `"level":"WARN"`) {
		t.Fatal("configured level was ignored")
	}
	if _, err = NewLogger(&out, "SECRET_INVALID_LEVEL"); err == nil || strings.Contains(err.Error(), "SECRET_INVALID_LEVEL") {
		t.Fatal("invalid log level was accepted or exposed")
	}
	var heartbeat Heartbeat
	heartbeat.Beat()
	if !heartbeat.Recent(time.Minute) {
		t.Fatal("recent heartbeat was lost")
	}
	old := time.Now().Add(-2 * time.Minute)
	heartbeat.last.Store(&old)
	if heartbeat.Recent(time.Minute) {
		t.Fatal("stale heartbeat still reported ready")
	}
	heartbeat.Beat()
	heartbeat.Clear()
	if heartbeat.Recent(time.Minute) {
		t.Fatal("stopped worker still reported ready")
	}
}

func TestRequestsSilencePanicPayload(t *testing.T) {
	var logs bytes.Buffer
	logger, err := NewLogger(&logs, "")
	if err != nil {
		t.Fatal(err)
	}
	previous := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(previous) })
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) { panic(errors.New("PANIC_SECRET")) })
	response := httptest.NewRecorder()
	Requests(mux, map[string]struct{}{"/": {}}, http.TimeoutHandler(mux, time.Second, "timeout")).ServeHTTP(response, httptest.NewRequest("GET", "/", nil))
	if response.Code != 500 || response.Header().Get(RequestIDHeader) == "" || strings.Contains(logs.String()+response.Body.String(), "PANIC_SECRET") {
		t.Fatal("panicked request lost correlation or exposed raw details")
	}
}

func TestRequestLogRejectsSynthesizedRedirectRoute(t *testing.T) {
	var logs bytes.Buffer
	logger, err := NewLogger(&logs, "")
	if err != nil {
		t.Fatal(err)
	}
	previous := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(previous) })
	mux := http.NewServeMux()
	mux.HandleFunc("/accounts/{id}/", func(http.ResponseWriter, *http.Request) {})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/accounts/PRIVATE_REDIRECT_TARGET", nil)
	request.Method = http.MethodConnect
	Requests(mux, map[string]struct{}{"/accounts/{id}/": {}}, mux).ServeHTTP(response, request)
	if response.Code < 300 || response.Code >= 400 || strings.Contains(logs.String(), "PRIVATE_REDIRECT_TARGET") {
		t.Fatal("redirect behavior changed or the synthesized route was logged")
	}
}
