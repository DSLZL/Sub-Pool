package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/database"
	"sub-pool/backend/internal/telemetry"
)

func readiness(runtime *database.Runtime, pool *pgxpool.Pool, manifest map[string]string, collector, notifications *telemetry.Heartbeat) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		reason := readinessReason(ctx, runtime, pool, manifest, collector, notifications)
		if reason != "" {
			telemetry.Log(ctx, slog.LevelWarn, "service not ready", slog.String("reason", reason))
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unavailable","scope":"readiness"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","scope":"readiness"}`))
	}
}

func readinessReason(ctx context.Context, runtime *database.Runtime, pool *pgxpool.Pool, manifest map[string]string, collector, notifications *telemetry.Heartbeat) string {
	release, ok := runtime.TryEnter()
	if !ok {
		return "runtime_unavailable"
	}
	defer release()
	if err := database.CheckMigrations(ctx, pool, manifest); err != nil {
		if errors.Is(err, database.ErrMigrationMismatch) {
			return "migration_mismatch"
		}
		telemetry.Error(ctx, "readiness_database", err)
		return "database_unavailable"
	}
	if !collector.Recent(time.Minute) {
		return "collector_heartbeat_missing"
	}
	if !notifications.Recent(time.Minute) {
		return "notification_heartbeat_missing"
	}
	return ""
}
