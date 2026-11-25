package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sub-pool/backend/internal/database"
	"sub-pool/backend/internal/telemetry"
)

type databasePools struct {
	requests *pgxpool.Pool
	workers  *pgxpool.Pool
	runtime  *database.Runtime
}

func (p *databasePools) close() {
	if p.runtime != nil {
		p.runtime.Close()
	}
	if p.workers != nil {
		p.workers.Close()
	}
	p.requests.Close()
}

func openDatabase(ctx context.Context, config *pgxpool.Config, serve bool, requestLimit string) (*databasePools, error) {
	config.MaxConns = 8
	if serve {
		config.MaxConns = 25
		if requestLimit != "" {
			limit, err := strconv.ParseInt(requestLimit, 10, 32)
			if err != nil || limit <= 0 {
				telemetry.Log(ctx, slog.LevelError, "database request pool limit must be a positive integer", slog.String("setting", "SUB_POOL_DB_MAX_CONNS"))
				return nil, errors.New("SUB_POOL_DB_MAX_CONNS must be a positive integer")
			}
			config.MaxConns = int32(limit)
		}
	}
	config.ConnConfig.ConnectTimeout = 5 * time.Second
	runtimeID := make([]byte, 16)
	if _, err := rand.Read(runtimeID); err != nil {
		return nil, errors.New("runtime identity unavailable")
	}
	config.ConnConfig.RuntimeParams["application_name"] = "sub-pool-" + base64.RawURLEncoding.EncodeToString(runtimeID)
	var owner atomic.Pointer[database.Runtime]
	config.BeforeAcquire = func(ctx context.Context, conn *pgx.Conn) bool {
		gate := owner.Load()
		return gate == nil || gate.CheckConnection(ctx, conn)
	}
	config.BeforeConnect = func(context.Context, *pgx.ConnConfig) error {
		if gate := owner.Load(); gate != nil && !gate.Available() {
			return database.ErrRuntimeUnavailable
		}
		return nil
	}
	requests, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		telemetry.Error(ctx, "database_pool", err)
		return nil, errors.New("database connection unavailable")
	}
	pools := &databasePools{requests: requests}
	lockPool := requests
	if serve {
		workerConfig := config.Copy()
		workerConfig.MaxConns = 8
		workerConfig.MinConns = 0
		workerConfig.MinIdleConns = 0
		pools.workers, err = pgxpool.NewWithConfig(ctx, workerConfig)
		if err != nil {
			pools.close()
			telemetry.Error(ctx, "worker_database_pool", err)
			return nil, errors.New("worker database connection unavailable")
		}
		lockPool = pools.workers
	}

	ready, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	pools.runtime, err = database.NewRuntime(ready, lockPool)
	if err == nil {
		owner.Store(pools.runtime)
		err = database.Migrate(ready, requests)
		if err != nil {
			telemetry.Error(ctx, "database_migrate", err)
		}
	} else {
		telemetry.Error(ctx, "database_runtime", err)
	}
	if err != nil {
		pools.close()
		return nil, errors.New("database connection or migration failed; inspect database health and migration version")
	}
	return pools, nil
}
