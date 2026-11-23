package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/backups"
	"sub-pool/backend/internal/connection"
	"sub-pool/backend/internal/database"
	"sub-pool/backend/internal/help"
	"sub-pool/backend/internal/notifications"
	"sub-pool/backend/internal/telemetry"
)

func readStdinPassword(stdin io.Reader) (string, error) {
	password, err := io.ReadAll(io.LimitReader(stdin, 131))
	if err != nil || len(password) > 130 {
		return "", errors.New("could not read a bounded password from stdin")
	}
	if bytes.HasPrefix(password, []byte{0xef, 0xbb, 0xbf}) {
		return "", errors.New("stdin password must be UTF-8 without a BOM; use raw stdin redirection")
	}
	value := strings.TrimSuffix(strings.TrimSuffix(string(password), "\n"), "\r")
	if !auth.ValidPassword(value) {
		return "", errors.New("password needs ten characters including an ASCII letter and digit, at most 128 UTF-8 bytes")
	}
	return value, nil
}

func initializeAdministrator(ctx context.Context, service *auth.Service, stdout io.Writer) error {
	var configured *string
	if value, present := os.LookupEnv("SUB_POOL_INITIAL_ADMIN_PASSWORD"); present {
		configured = &value
	}
	result, err := service.EnsureInitialAdmin(ctx, configured)
	if errors.Is(err, auth.ErrInput) {
		telemetry.Log(ctx, slog.LevelError, "initial administrator password does not meet the password policy",
			slog.String("setting", "SUB_POOL_INITIAL_ADMIN_PASSWORD"))
		return err
	}
	if err != nil {
		telemetry.Error(ctx, "administrator_initialization", err)
		return errors.New("administrator initialization failed. Inspect database health")
	}
	if result.GeneratedPassword != "" {
		if err = json.NewEncoder(stdout).Encode(map[string]string{"event": "initial_admin_password", "username": "admin", "password": result.GeneratedPassword}); err != nil {
			telemetry.Log(ctx, slog.LevelError, "initial password delivery failed", slog.String("recovery", "sub-pool reset-password <username>"))
			return errors.New("initial password delivery failed")
		}
	}
	if result.LegacyRevoked != 0 {
		if err = json.NewEncoder(stdout).Encode(map[string]any{
			"event": "legacy_initial_password_revoked", "affected": result.LegacyRevoked, "recovery": "sub-pool reset-password <username>",
		}); err != nil {
			telemetry.Log(ctx, slog.LevelError, "legacy password notice delivery failed", slog.String("recovery", "sub-pool reset-password <username>"))
			return errors.New("legacy password notice delivery failed")
		}
	}
	return nil
}

func durationSetting(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration such as %s", name, fallback)
	}
	return duration, nil
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	usesPassword := len(args) == 2 && (args[0] == "bootstrap" || args[0] == "reset-password")
	if !usesPassword && !(len(args) == 1 && (args[0] == "serve" || args[0] == "migrate")) {
		return errors.New("usage: sub-pool serve | migrate | bootstrap <username> | reset-password <username> (password on stdin)")
	}
	var readTimeout, connectTimeout, backupTimeout time.Duration
	backupMaxBytes := backups.DefaultMaxBytes
	var upstreamWritesDisabled bool
	if args[0] == "serve" {
		var err error
		if value := os.Getenv("SUB_POOL_DISABLE_UPSTREAM_WRITES"); value != "" {
			upstreamWritesDisabled, err = strconv.ParseBool(value)
			if err != nil {
				return errors.New("SUB_POOL_DISABLE_UPSTREAM_WRITES must be a boolean")
			}
		}
		readTimeout, err = durationSetting("SUB_POOL_UPSTREAM_READ_TIMEOUT", connection.DefaultUpstreamReadTimeout)
		if err == nil {
			connectTimeout, err = durationSetting("SUB_POOL_UPSTREAM_CONNECT_TIMEOUT", connection.DefaultUpstreamConnectTimeout)
		}
		if err == nil {
			backupTimeout, err = durationSetting("SUB_POOL_BACKUP_TIMEOUT", 10*time.Minute)
		}
		if err != nil {
			telemetry.Log(ctx, slog.LevelError, err.Error())
			return err
		}
		if value := os.Getenv("SUB_POOL_BACKUP_MAX_BYTES"); value != "" {
			backupMaxBytes, err = strconv.ParseInt(value, 10, 64)
			if err != nil || backupMaxBytes <= 0 {
				return errors.New("SUB_POOL_BACKUP_MAX_BYTES must be a positive integer")
			}
		}
	}
	var password string
	if usesPassword {
		var err error
		password, err = readStdinPassword(stdin)
		if err != nil {
			return err
		}
	}
	config, err := pgxpool.ParseConfig(os.Getenv("SUB_POOL_DATABASE_URL"))
	if err != nil || os.Getenv("SUB_POOL_DATABASE_URL") == "" {
		telemetry.Log(ctx, slog.LevelError, "invalid database configuration", slog.String("setting", "SUB_POOL_DATABASE_URL"))
		return errors.New("database configuration is missing or invalid")
	}
	config.MaxConns = 8
	config.ConnConfig.ConnectTimeout = 5 * time.Second
	runtimeID := make([]byte, 16)
	if _, err = rand.Read(runtimeID); err != nil {
		return errors.New("runtime identity unavailable")
	}
	config.ConnConfig.RuntimeParams["application_name"] = "sub-pool-" + base64.RawURLEncoding.EncodeToString(runtimeID)
	var runtimeOwner atomic.Pointer[database.Runtime]
	config.BeforeAcquire = func(ctx context.Context, conn *pgx.Conn) bool {
		gate := runtimeOwner.Load()
		return gate == nil || gate.CheckConnection(ctx, conn)
	}
	config.BeforeConnect = func(context.Context, *pgx.ConnConfig) error {
		if gate := runtimeOwner.Load(); gate != nil && !gate.Available() {
			return database.ErrRuntimeUnavailable
		}
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		telemetry.Error(ctx, "database_pool", err)
		return errors.New("database connection unavailable")
	}
	defer pool.Close()
	ready, cancel := context.WithTimeout(ctx, 15*time.Second)
	runtimeGate, err := database.NewRuntime(ready, pool)
	if err == nil {
		runtimeOwner.Store(runtimeGate)
		defer runtimeGate.Close()
		err = database.Migrate(ready, pool)
		if err != nil {
			telemetry.Error(ctx, "database_migrate", err)
		}
	} else {
		telemetry.Error(ctx, "database_runtime", err)
	}
	cancel()
	if err != nil {
		return errors.New("database connection or migration failed; inspect database health and migration version")
	}
	service := auth.New(pool)
	switch args[0] {
	case "migrate":
		_, err = fmt.Fprintln(stdout, "Migrations applied.")
		return err
	case "bootstrap":
		_, err = service.Bootstrap(ctx, args[1], password)
		if errors.Is(err, auth.ErrAlreadyInitialized) {
			return errors.New("administrator already exists; no credentials changed")
		}
		if err != nil {
			telemetry.Error(ctx, "bootstrap_admin", err)
			return errors.New("administrator initialization failed; verify username/password policy and database health")
		}
		_, err = fmt.Fprintln(stdout, "Administrator initialized.")
		return err
	case "reset-password":
		err = service.ResetAdministratorPassword(ctx, args[1], password)
		switch {
		case errors.Is(err, auth.ErrNotFound):
			return errors.New("enabled administrator not found")
		case errors.Is(err, auth.ErrInput):
			return errors.New("use a valid username and a different password")
		case err != nil:
			telemetry.Error(ctx, "reset_admin_password", err)
			return errors.New("administrator password reset failed; check database health and verify the result before retrying")
		}
		_, err = fmt.Fprintln(stdout, "Administrator password reset.")
		return err
	}
	handler, err := auth.NewHandler(service, os.Getenv("SUB_POOL_PUBLIC_ORIGIN"), os.Getenv("SUB_POOL_ALLOW_LOOPBACK_HTTP") == "true")
	if err != nil {
		telemetry.Error(ctx, "public_origin", err)
		return err
	}
	if err = handler.ConfigureSources(os.Getenv("SUB_POOL_TRUSTED_PROXY_CIDRS")); err != nil {
		telemetry.Error(ctx, "trusted_proxy_configuration", err)
		return err
	}
	key, err := base64.StdEncoding.DecodeString(os.Getenv("SUB_POOL_SECRET_KEY"))
	if err != nil {
		telemetry.Error(ctx, "secret_key_configuration", err)
		return errors.New("secret storage key must be base64-encoded 32 bytes")
	}
	connections, err := connection.NewWithUpstreamTimeouts(pool, service, key, os.Getenv("SUB_POOL_ALLOW_LOOPBACK_HTTP") == "true", readTimeout, connectTimeout)
	if err != nil {
		telemetry.Error(ctx, "connection_service", err)
		return err
	}
	mail, err := notifications.New(pool, service, key, os.Getenv("SUB_POOL_ALLOW_LOOPBACK_HTTP") == "true")
	if err != nil {
		telemetry.Error(ctx, "notification_service", err)
		return err
	}
	recovery, err := backups.New(pool, service, runtimeGate, key)
	if err != nil {
		telemetry.Error(ctx, "backup_service", err)
		return err
	}
	recovery.MaxBytes = backupMaxBytes
	if err = recovery.Recover(ctx); err != nil {
		telemetry.Error(ctx, "restore_recovery", err)
		return errors.New("restore recovery unavailable; inspect persisted restore plans")
	}
	mux := http.NewServeMux()
	routes := make(map[string]struct{})
	register := func(pattern string, handler http.Handler) {
		mux.Handle(pattern, handler)
		routes[pattern] = struct{}{}
	}
	register("/api/v1/help/", &help.Handler{Service: help.New(pool, service), Boundary: handler})
	register("/api/v1/backups/", &backups.Handler{Service: recovery, Boundary: handler})
	connections.Runtime = runtimeGate
	connections.UpstreamWritesDisabled = upstreamWritesDisabled
	mail.Financial = connections
	if err = connections.ConfigureSimulation(os.Getenv("SUB_POOL_SIMULATION_BINDING")); err != nil {
		telemetry.Error(ctx, "simulation_configuration", err)
		return err
	}
	mail.Runtime = runtimeGate
	connectionHandler := &connection.Handler{Service: connections, Boundary: handler}
	for _, path := range []string{"/api/v1/connection", "/api/v1/connection/", "/api/v1/settings", "/api/v1/settings/"} {
		register(path, connectionHandler)
	}
	register("/api/v1/accounts/", &connection.AccountsHandler{Service: connections, Boundary: handler})
	register("/api/v1/monitoring/", &connection.MonitoringHandler{Service: connections, Boundary: handler})
	register("/api/v1/observations/", &connection.ObservationsHandler{Service: connections, Boundary: handler})
	register("/api/v1/estimates/", &connection.EstimatesHandler{Service: connections, Boundary: handler})
	register("/api/v1/maintenance/", &connection.MaintenanceHandler{Service: connections, Boundary: handler})
	register("/api/v1/wallets/", &connection.WalletsHandler{Service: connections, Boundary: handler})
	register("/api/v1/wallet-applications/", &connection.WalletApplicationsHandler{Service: connections, Boundary: handler})
	register("/api/v1/temporary/", &connection.TemporaryHandler{Service: connections, Boundary: handler})
	register("/api/v1/pricing/", &connection.PricingHandler{Service: connections, Boundary: handler})
	register("/api/v1/dispatch/", &connection.DispatchHandler{Service: connections, Boundary: handler})
	register("/api/v1/usage/", &connection.UsageHandler{Service: connections, Boundary: handler})
	register("/api/v1/participants/", &connection.ParticipantsHandler{Service: connections, Boundary: handler})
	register("/api/v1/pools/", &connection.PoolsHandler{Service: connections, Boundary: handler})
	register("/api/v1/notifications/", &notifications.Handler{Service: mail, Boundary: handler})
	register("/api/v1/api-keys/", handler)
	register("/health", http.HandlerFunc(connection.ProcessHealth))
	manifest, err := database.MigrationManifest()
	if err != nil {
		telemetry.Error(ctx, "migration_manifest", err)
		return errors.New("migration source unavailable")
	}
	register("/health/ready", readiness(runtimeGate, pool, manifest, &connections.Heartbeat, &mail.Heartbeat))
	register("/", handler)
	address := os.Getenv("SUB_POOL_LISTEN")
	if address == "" {
		address = "127.0.0.1:8081"
	}
	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" && r.URL.Path != "/health/ready" && !(backups.LongOperation(r) && r.URL.Path == "/api/v1/backups/apply") {
			release, err := runtimeGate.Enter(r.Context())
			if err != nil {
				telemetry.Error(r.Context(), "request_runtime", err)
				http.Error(w, "Runtime changed; read current state before another action.", http.StatusServiceUnavailable)
				return
			}
			defer release()
		}
		mux.ServeHTTP(w, r)
	})
	server := &http.Server{
		Addr: address, Handler: telemetry.Requests(mux, routes, backupDeadlines(protected, backupTimeout)),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	if err = initializeAdministrator(ctx, service, stdout); err != nil {
		return err
	}
	stopped := make(chan error, 1)
	collectorCtx, stopCollector := context.WithCancel(ctx)
	collectorDone := make(chan struct{})
	go func() { defer close(collectorDone); connections.RunCollector(collectorCtx) }()
	defer func() { stopCollector(); <-collectorDone }()
	mailDone := make(chan struct{})
	go func() { defer close(mailDone); mail.Run(collectorCtx) }()
	defer func() { stopCollector(); <-mailDone }()
	sourceExpiryDone := make(chan struct{})
	go func() { defer close(sourceExpiryDone); service.RunSourceExpiry(collectorCtx, runtimeGate) }()
	defer func() { stopCollector(); <-sourceExpiryDone }()
	slog.Info("sub-pool starting")
	go func() { stopped <- server.ListenAndServe() }()
	select {
	case err = <-stopped:
		if !errors.Is(err, http.ErrServerClosed) {
			telemetry.Error(ctx, "http_listen", err)
			return errors.New("HTTP service stopped unexpectedly")
		}
		return nil
	case <-ctx.Done():
		deadline, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err = server.Shutdown(deadline); err != nil {
			telemetry.Error(ctx, "http_shutdown", err)
			_ = server.Close()
			return errors.New("HTTP service shutdown timed out")
		}
		return nil
	}
}

func backupDeadlines(next http.Handler, timeout time.Duration) http.Handler {
	ordinary := http.TimeoutHandler(next, 15*time.Second, `{"message":"Request timed out; outcome unknown."}`)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !backups.LongOperation(r) {
			ordinary.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		deadline, _ := ctx.Deadline()
		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(deadline); err != nil {
			telemetry.Error(ctx, "backup_read_deadline", err)
			http.Error(w, "Backup transport unavailable.", http.StatusServiceUnavailable)
			return
		}
		if err := controller.SetWriteDeadline(deadline); err != nil {
			telemetry.Error(ctx, "backup_write_deadline", err)
			http.Error(w, "Backup transport unavailable.", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func main() {
	logger, err := telemetry.NewLogger(os.Stderr, os.Getenv("SUB_POOL_LOG_LEVEL"))
	slog.SetDefault(logger)
	if err != nil {
		slog.Error("invalid log level", "setting", "SUB_POOL_LOG_LEVEL")
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		if len(os.Args) > 1 && os.Args[1] == "serve" {
			slog.Error("sub-pool stopped")
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}
