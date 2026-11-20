package connection

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sub-pool/backend/internal/telemetry"
)

func TestUpstream500LogsOperationAndStatusWithoutSecrets(t *testing.T) {
	const secret = "SECRET_NATIVE_ADMIN_KEY"
	const text = "SECRET_NATIVE_RESPONSE"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != secret {
			t.Error("fixture did not receive its key")
		}
		http.Error(w, text, http.StatusInternalServerError)
	}))
	defer upstream.Close()

	var log bytes.Buffer
	logger, err := telemetry.NewLogger(&log, "info")
	if err != nil {
		t.Fatal(err)
	}
	previous := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(previous) })
	_, check := newUpstreamClient().get(context.Background(), upstream.URL, "/public-path?marker=SECRET_PATH", "api_key", secret, "usage.read", true)
	if check.State != "failed" || check.Reason != "UPSTREAM_ERROR" {
		t.Fatal("failure classification changed")
	}
	for _, hidden := range []string{secret, text, "SECRET_PATH", upstream.URL} {
		if strings.Contains(log.String(), hidden) {
			t.Fatal("upstream diagnostic exposed credentials, path or raw response")
		}
	}
	var event map[string]any
	if err := json.Unmarshal(log.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event["operation"] != "usage.read" || event["upstream_status"] != float64(500) || event["level"] != "ERROR" {
		t.Fatal("upstream diagnostic failed to identify the operation and status")
	}
}
