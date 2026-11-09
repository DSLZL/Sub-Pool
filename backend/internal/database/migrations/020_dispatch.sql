CREATE TABLE dispatch_pauses (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id text NOT NULL REFERENCES managed_accounts(id),
    actor_id text NOT NULL REFERENCES auth_users(id),
    request_id text NOT NULL CHECK (request_id ~ '^[A-Za-z0-9_-]{21}[AQgw]$'),
    request_digest text NOT NULL CHECK (length(request_digest) = 64),
    source_id text NOT NULL REFERENCES connection_sources(id),
    connection_revision bigint NOT NULL CHECK (connection_revision > 0),
    account_revision bigint NOT NULL CHECK (account_revision > 0),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    state text NOT NULL CHECK (state IN ('pausing','paused','restoring','restored','failed','needs_review','reviewed')),
    reason text NOT NULL,
    previous_schedulable boolean NOT NULL,
    native_updated_at timestamptz NOT NULL,
    restore_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (actor_id,request_id)
);
CREATE UNIQUE INDEX dispatch_one_active_account ON dispatch_pauses(account_id)
    WHERE state IN ('pausing','paused','restoring','needs_review');
CREATE INDEX dispatch_due ON dispatch_pauses(restore_at,id) WHERE state='paused';

CREATE TABLE dispatch_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    pause_id bigint NOT NULL REFERENCES dispatch_pauses(id),
    account_id text NOT NULL REFERENCES managed_accounts(id),
    actor_id text REFERENCES auth_users(id),
    actor text NOT NULL CHECK (actor IN ('administrator','worker')),
    action text NOT NULL CHECK (action IN ('pause','pause_result','reschedule','restore','restore_result','review','interrupted','retry')),
    occurred_at timestamptz NOT NULL,
    snapshot jsonb NOT NULL
);
CREATE INDEX dispatch_account_history ON dispatch_events(account_id,id);
