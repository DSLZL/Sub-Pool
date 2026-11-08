package database

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const runtimeLock int64 = 0x5350324152554e

var (
	ErrRuntimeBusy        = errors.New("another database client or runtime is present")
	ErrRuntimeChanged     = errors.New("runtime changed while the request was waiting")
	ErrRuntimeUnavailable = errors.New("runtime exclusion is unavailable")
	runtimeName           = regexp.MustCompile(`^sub-pool-[A-Za-z0-9_-]{16,48}$`)
)

// Runtime drains local requests/workers and excludes other live service sessions during restore.
type Runtime struct {
	mu         sync.RWMutex
	generation atomic.Uint64
	closed     atomic.Bool
	closeOnce  sync.Once
	conn       *pgxpool.Conn
	name       string
	pid        uint32
	startedAt  time.Time
}

func NewRuntime(ctx context.Context, pool *pgxpool.Pool) (*Runtime, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, ErrRuntimeUnavailable
	}
	var name string
	var locked bool
	var startedAt time.Time
	err = conn.QueryRow(ctx, `SELECT current_setting('application_name'),pg_try_advisory_lock_shared($1),(SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid())`, runtimeLock).Scan(&name, &locked, &startedAt)
	if err != nil || !locked || !runtimeName.MatchString(name) {
		if err != nil || locked {
			cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = conn.Conn().Close(cleanup)
			cancel()
		}
		conn.Release()
		if err == nil && !locked {
			return nil, ErrRuntimeBusy
		}
		return nil, ErrRuntimeUnavailable
	}
	return &Runtime{conn: conn, name: name, pid: conn.Conn().PgConn().PID(), startedAt: startedAt}, nil
}

func (r *Runtime) Name() string { return r.name }

func (r *Runtime) Available() bool { return r != nil && !r.closed.Load() }

// Pool acquisition must prove the lifetime barrier survived a database reconnect.
func (r *Runtime) CheckConnection(ctx context.Context, conn *pgx.Conn) bool {
	if r == nil || r.closed.Load() {
		return false
	}
	var alive bool
	err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND backend_start=$2 AND application_name=$3 AND datname=current_database())`, r.pid, r.startedAt, r.name).Scan(&alive)
	if err != nil {
		return false
	}
	if !alive {
		r.closed.Store(true)
	}
	return alive && !r.closed.Load()
}

// A nil gate is used only by isolated domain tests; production wires the same gate everywhere.
func (r *Runtime) Enter(ctx context.Context) (func(), error) {
	if r == nil {
		return func() {}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	version := r.generation.Load()
	r.mu.RLock()
	if r.closed.Load() || r.generation.Load() != version || ctx.Err() != nil {
		r.mu.RUnlock()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if r.closed.Load() {
			return nil, ErrRuntimeUnavailable
		}
		return nil, ErrRuntimeChanged
	}
	return r.mu.RUnlock, nil
}

// Exclusive keeps its own shared session lock while upgrading; another runtime cannot slip in.
func (r *Runtime) Exclusive(ctx context.Context, run func(*pgxpool.Conn) error) (err error) {
	if r == nil {
		return ErrRuntimeUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed.Load() || ctx.Err() != nil {
		return ErrRuntimeUnavailable
	}
	r.generation.Add(1)
	defer r.generation.Add(1)
	var locked bool
	if err = r.conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, runtimeLock).Scan(&locked); err != nil {
		r.closed.Store(true)
		return ErrRuntimeUnavailable
	}
	if !locked {
		return ErrRuntimeBusy
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var unlocked bool
		if e := r.conn.QueryRow(cleanup, `SELECT pg_advisory_unlock($1)`, runtimeLock).Scan(&unlocked); e != nil || !unlocked {
			r.closed.Store(true)
			_ = r.conn.Conn().Close(cleanup)
			err = ErrRuntimeUnavailable
		}
	}()
	var foreign bool
	if err = r.conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND backend_type='client backend' AND application_name IS DISTINCT FROM $1)`, r.name).Scan(&foreign); err != nil {
		return ErrRuntimeUnavailable
	}
	if foreign {
		return ErrRuntimeBusy
	}
	return run(r.conn)
}

func (r *Runtime) Close() {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		r.closed.Store(true)
		r.mu.Lock()
		defer r.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var unlocked bool
		if err := r.conn.QueryRow(ctx, `SELECT pg_advisory_unlock_shared($1)`, runtimeLock).Scan(&unlocked); err != nil || !unlocked {
			_ = r.conn.Conn().Close(ctx)
		}
		r.conn.Release()
	})
}
