package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func NewLogger(out io.Writer, level string) (*slog.Logger, error) {
	var threshold slog.Level
	var err error
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		threshold = slog.LevelDebug
	case "", "info":
		threshold = slog.LevelInfo
	case "warn":
		threshold = slog.LevelWarn
	case "error":
		threshold = slog.LevelError
	default:
		err = errors.New("SUB_POOL_LOG_LEVEL must be debug, info, warn or error")
	}
	return slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{Level: threshold})), err
}

func Log(ctx context.Context, level slog.Level, message string, attrs ...slog.Attr) {
	if trace, ok := ctx.Value(traceKey{}).(*requestTrace); ok {
		attrs = append(attrs, slog.String("request_id", trace.id))
		if actor := trace.actor.Load(); actor != nil {
			attrs = append(attrs, slog.String("user_id", *actor))
		}
	}
	slog.LogAttrs(ctx, level, message, attrs...)
}

var sqlState = regexp.MustCompile(`^[0-9A-Z]{5}$`)

// Error records the cause without logging error text, which can contain secrets.
func Error(ctx context.Context, operation string, err error, attrs ...slog.Attr) {
	attrs = append(attrs, slog.String("operation", operation), slog.String("error_type", fmt.Sprintf("%T", err)))
	var database *pgconn.PgError
	var connection *pgconn.ConnectError
	var network net.Error
	switch {
	case errors.Is(err, context.Canceled):
		attrs = append(attrs, slog.String("error_kind", "canceled"))
	case errors.Is(err, context.DeadlineExceeded):
		attrs = append(attrs, slog.String("error_kind", "timeout"))
	case errors.As(err, &database):
		attrs = append(attrs, slog.String("error_kind", "database"))
		if sqlState.MatchString(database.Code) {
			attrs = append(attrs, slog.String("sqlstate", database.Code))
		}
	case errors.As(err, &connection):
		attrs = append(attrs, slog.String("error_kind", "database_connection"))
	case errors.As(err, &network):
		kind := "network"
		if network.Timeout() {
			kind = "timeout"
		}
		attrs = append(attrs, slog.String("error_kind", kind))
	}
	Log(ctx, slog.LevelError, "operation failed", attrs...)
}

// StartTask returns the recorder for one background attempt.
func StartTask(ctx context.Context, name string) func(err error, skip string) {
	start := time.Now()
	Log(ctx, slog.LevelDebug, "background task started", slog.String("task", name))
	return func(err error, skip string) {
		attrs := []slog.Attr{slog.String("task", name), slog.Int64("duration_ms", time.Since(start).Milliseconds())}
		if err != nil && ctx.Err() == nil {
			Error(ctx, "background_task", err, attrs...)
			return
		}
		if ctx.Err() != nil {
			skip = "stopping"
		}
		if skip != "" {
			Log(ctx, slog.LevelDebug, "background task skipped", append(attrs, slog.String("reason", skip))...)
			return
		}
		Log(ctx, slog.LevelDebug, "background task completed", attrs...)
	}
}

func RunStep(ctx context.Context, name string, run func() error) error {
	done := StartTask(ctx, name)
	err := run()
	done(err, "")
	return err
}

func RunJob(ctx context.Context, name string, run func() (bool, error)) (bool, error) {
	done := StartTask(ctx, name)
	worked, err := run()
	skip := ""
	if !worked {
		skip = "no_work_due"
	}
	done(err, skip)
	return worked, err
}
