package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
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
)

func readBootstrapPassword(stdin io.Reader) (string, error) {
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

func run(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "serve" && args[0] != "migrate" && args[0] != "bootstrap" || args[0] != "bootstrap" && len(args) != 1 || args[0] == "bootstrap" && len(args) != 2 {
		return errors.New("usage: sub-pool serve | migrate | bootstrap <username> (password on stdin)")
	}
	var password string
	if args[0] == "bootstrap" {
		var err error
		password, err = readBootstrapPassword(stdin)
		if err != nil {
			return err
		}
	}
	config, err := pgxpool.ParseConfig(os.Getenv("SUB_POOL_DATABASE_URL"))
	if err != nil || os.Getenv("SUB_POOL_DATABASE_URL") == "" {
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
		return errors.New("database connection unavailable")
	}
	defer pool.Close()
	ready, cancel := context.WithTimeout(ctx, 15*time.Second)
	runtimeGate, err := database.NewRuntime(ready, pool)
	if err == nil {
		runtimeOwner.Store(runtimeGate)
		defer runtimeGate.Close()
		err = database.Migrate(ready, pool)
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
			return errors.New("administrator initialization failed; verify username/password policy and database health")
		}
		_, err = fmt.Fprintln(stdout, "Administrator initialized.")
		return err
	}
	handler, err := auth.NewHandler(service, os.Getenv("SUB_POOL_PUBLIC_ORIGIN"), os.Getenv("SUB_POOL_ALLOW_LOOPBACK_HTTP") == "true")
	if err != nil {
		return err
	}
	if err = handler.ConfigureSources(os.Getenv("SUB_POOL_TRUSTED_PROXY_CIDRS")); err != nil {
		return err
	}
	key, err := base64.StdEncoding.DecodeString(os.Getenv("SUB_POOL_SECRET_KEY"))
	if err != nil {
		return errors.New("secret storage key must be base64-encoded 32 bytes")
	}
	connections, err := connection.New(pool, service, key, os.Getenv("SUB_POOL_ALLOW_LOOPBACK_HTTP") == "true")
	if err != nil {
		return err
	}
	mail, err := notifications.New(pool, service, key, os.Getenv("SUB_POOL_ALLOW_LOOPBACK_HTTP") == "true")
	if err != nil {
		return err
	}
	recovery, err := backups.New(pool, service, runtimeGate, key)
	if err != nil {
		return err
	}
	if err = recovery.Recover(ctx); err != nil {
		return errors.New("restore recovery unavailable; inspect persisted restore plans")
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/help/", &help.Handler{Service: help.New(pool, service), Boundary: handler})
	mux.Handle("/api/v1/backups/", &backups.Handler{Service: recovery, Boundary: handler})
	connections.Runtime = runtimeGate
	if err = connections.ConfigureDispatchControl(os.Getenv("SUB_POOL_SCHEDULING_CONTROL_BINDING")); err != nil {
		return err
	}
	mail.Runtime = runtimeGate
	connectionHandler := &connection.Handler{Service: connections, Boundary: handler}
	for _, path := range []string{"/api/v1/connection", "/api/v1/connection/", "/api/v1/settings", "/api/v1/settings/"} {
		mux.Handle(path, connectionHandler)
	}
	mux.Handle("/api/v1/accounts/", &connection.AccountsHandler{Service: connections, Boundary: handler})
	mux.Handle("/api/v1/monitoring/", &connection.MonitoringHandler{Service: connections, Boundary: handler})
	mux.Handle("/api/v1/observations/", &connection.ObservationsHandler{Service: connections, Boundary: handler})
	mux.Handle("/api/v1/estimates/", &connection.EstimatesHandler{Service: connections, Boundary: handler})
	mux.Handle("/api/v1/maintenance/", &connection.MaintenanceHandler{Service: connections, Boundary: handler})
	mux.Handle("/api/v1/wallets/", &connection.WalletsHandler{Service: connections, Boundary: handler})
	mux.Handle("/api/v1/wallet-applications/", &connection.WalletApplicationsHandler{Service: connections, Boundary: handler})
	mux.Handle("/api/v1/temporary/", &connection.TemporaryHandler{Service: connections, Boundary: handler})
	mux.Handle("/api/v1/pricing/", &connection.PricingHandler{Service: connections, Boundary: handler})
	mux.Handle("/api/v1/dispatch/", &connection.DispatchHandler{Service: connections, Boundary: handler})
	mux.Handle("/api/v1/usage/", &connection.UsageHandler{Service: connections, Boundary: handler})
	mux.Handle("/api/v1/participants/", &connection.ParticipantsHandler{Service: connections, Boundary: handler})
	mux.Handle("/api/v1/pools/", &connection.PoolsHandler{Service: connections, Boundary: handler})
	mux.Handle("/api/v1/notifications/", &notifications.Handler{Service: mail, Boundary: handler})
	mux.Handle("/api/v1/api-keys/", handler)
	mux.HandleFunc("/health", connection.ProcessHealth)
	mux.Handle("/", handler)
	address := os.Getenv("SUB_POOL_LISTEN")
	if address == "" {
		address = "127.0.0.1:8081"
	}
	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" && !(r.Method == http.MethodPost && r.URL.Path == "/api/v1/backups/apply") {
			release, err := runtimeGate.Enter(r.Context())
			if err != nil {
				http.Error(w, "Runtime changed; read current state before another action.", http.StatusServiceUnavailable)
				return
			}
			defer release()
		}
		mux.ServeHTTP(w, r)
	})
	server := &http.Server{
		Addr: address, Handler: http.TimeoutHandler(protected, 15*time.Second, `{"message":"Request timed out; outcome unknown."}`),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	stopped := make(chan error, 1)
	collectorCtx, stopCollector := context.WithCancel(ctx)
	collectorDone := make(chan struct{})
	go func() { defer close(collectorDone); connections.RunCollector(collectorCtx) }()
	defer func() { stopCollector(); <-collectorDone }()
	mailDone := make(chan struct{})
	go func() { defer close(mailDone); mail.Run(collectorCtx) }()
	defer func() { stopCollector(); <-mailDone }()
	go func() { stopped <- server.ListenAndServe() }()
	select {
	case err = <-stopped:
		if !errors.Is(err, http.ErrServerClosed) {
			return errors.New("HTTP service stopped unexpectedly")
		}
		return nil
	case <-ctx.Done():
		deadline, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err = server.Shutdown(deadline); err != nil {
			_ = server.Close()
			return errors.New("HTTP service shutdown timed out")
		}
		return nil
	}
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
