package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/database"
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
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return errors.New("database connection unavailable")
	}
	defer pool.Close()
	ready, cancel := context.WithTimeout(ctx, 15*time.Second)
	err = pool.Ping(ready)
	if err == nil {
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
	address := os.Getenv("SUB_POOL_LISTEN")
	if address == "" {
		address = "127.0.0.1:8081"
	}
	server := &http.Server{
		Addr: address, Handler: http.TimeoutHandler(handler, 15*time.Second, `{"message":"Request timed out; outcome unknown."}`),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	stopped := make(chan error, 1)
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
