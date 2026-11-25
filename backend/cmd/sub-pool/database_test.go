package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/testdb"
)

func TestInvalidDatabaseRequestLimits(t *testing.T) {
	for _, value := range []string{"0", "-2", "abc", "2147483648"} {
		config, err := pgxpool.ParseConfig("postgres://postgres@127.0.0.1:1/unavailable?sslmode=disable")
		if err != nil {
			t.Fatal(err)
		}
		pools, err := openDatabase(context.Background(), config, true, value)
		if pools != nil {
			pools.close()
		}
		if err == nil || err.Error() != "SUB_POOL_DB_MAX_CONNS must be a positive integer" {
			t.Fatalf("invalid capacity %q: %v", value, err)
		}
	}
}

func TestDatabasePoolOverrideAndSingleCommand(t *testing.T) {
	fixture := testdb.Open(t)
	serve, err := openDatabase(context.Background(), fixture.Config(), true, "12")
	if err != nil {
		t.Fatal(err)
	}
	requestLimit, workerLimit := serve.requests.Stat().MaxConns(), serve.workers.Stat().MaxConns()
	serve.close()
	if requestLimit != 12 || workerLimit != 8 {
		t.Fatalf("override changed the wrong budget: %d, %d", requestLimit, workerLimit)
	}
	command, err := openDatabase(context.Background(), fixture.Config(), false, "0")
	if err != nil {
		t.Fatal(err)
	}
	defer command.close()
	if command.workers != nil || command.requests.Stat().MaxConns() != 8 || command.requests.Stat().AcquiredConns() != 1 {
		t.Fatal("single command did not retain its original database lifetime")
	}
}

func TestDatabasePoolsIsolateReadsAndShareRuntime(t *testing.T) {
	fixture := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	pools, err := openDatabase(ctx, fixture.Config(), true, "")
	if err != nil {
		t.Fatal(err)
	}
	defer pools.close()
	if pools.requests.Stat().MaxConns() != 25 || pools.workers.Stat().MaxConns() != 8 ||
		pools.requests.Stat().AcquiredConns() != 0 || pools.workers.Stat().AcquiredConns() != 1 {
		t.Fatal("runtime reservation or default pool capacity is incorrect")
	}
	for _, pool := range []*pgxpool.Pool{pools.requests, pools.workers} {
		var application string
		err = pool.QueryRow(ctx, `SELECT current_setting('application_name')`).Scan(&application)
		if err != nil || application != pools.runtime.Name() {
			t.Fatalf("connection does not belong to the runtime: %q, %v", application, err)
		}
	}

	const admin, password = "pool.admin", "PoolFixtureStrongPassword1"
	identity := auth.New(pools.requests)
	if _, err = identity.Bootstrap(ctx, admin, password); err != nil {
		t.Fatal(err)
	}
	_, token, err := identity.Login(ctx, admin, password, netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := auth.NewHandler(identity, "http://127.0.0.1:8081", true)
	if err != nil {
		t.Fatal(err)
	}
	held := make([]*pgxpool.Conn, 0, 7)
	defer func() {
		for _, conn := range held {
			conn.Release()
		}
	}()
	for range cap(held) {
		conn, err := pools.workers.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
	}
	bounded, stop := context.WithTimeout(ctx, 150*time.Millisecond)
	extra, overflow := pools.workers.Acquire(bounded)
	stop()
	if extra != nil {
		extra.Release()
	}
	if !errors.Is(overflow, context.DeadlineExceeded) || pools.workers.Stat().AcquiredConns() != 8 {
		t.Fatalf("worker budget was not saturated: %v", overflow)
	}

	readCtx, done := context.WithTimeout(ctx, 15*time.Second)
	defer done()
	start := make(chan struct{})
	results := make(chan error, 50)
	for range 50 {
		go func() {
			<-start
			request := httptest.NewRequestWithContext(readCtx, http.MethodGet, "/api/v1/access", nil)
			request.AddCookie(&http.Cookie{Name: "sub_pool_dev_session", Value: token})
			reply := httptest.NewRecorder()
			handler.ServeHTTP(reply, request)
			var access auth.Access
			if reply.Code != http.StatusOK || json.Unmarshal(reply.Body.Bytes(), &access) != nil || access.User.Username != admin {
				results <- fmt.Errorf("access read returned HTTP %d or unexpected authority", reply.Code)
				return
			}
			results <- nil
		}()
	}
	close(start)
	for range 50 {
		if err = <-results; err != nil {
			t.Error(err)
		}
	}
	if readCtx.Err() != nil {
		t.Fatal("read load exceeded the request timeout")
	}
	if err = pools.runtime.Exclusive(ctx, func(*pgxpool.Conn) error { return nil }); err != nil {
		t.Fatal("exclusive work treated one of its own pools as a foreign client", err)
	}
	for _, conn := range held {
		conn.Release()
	}
	held = nil

	var terminated bool
	err = pools.requests.QueryRow(ctx, `SELECT pg_terminate_backend(l.pid,5000)
		FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid WHERE
		l.locktype='advisory' AND l.mode='ShareLock' AND l.granted
		AND a.datname=current_database() AND a.application_name=$1`, pools.runtime.Name()).Scan(&terminated)
	if err != nil || !terminated {
		t.Fatalf("could not close the lifetime session: %v", err)
	}
	for _, pool := range []*pgxpool.Pool{pools.requests, pools.workers} {
		probe, cancel := context.WithTimeout(ctx, time.Second)
		err = pool.Ping(probe)
		cancel()
		if err == nil {
			t.Fatal("a pool still accepted work after losing the runtime session")
		}
	}
	if pools.runtime.Available() {
		t.Fatal("the runtime incorrectly stayed available")
	}
}
