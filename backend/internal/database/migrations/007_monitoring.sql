CREATE TABLE monitoring_config (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    enabled boolean NOT NULL DEFAULT false,
    interval_seconds integer NOT NULL DEFAULT 300 CHECK (interval_seconds BETWEEN 60 AND 86400),
    stale_after_seconds integer NOT NULL DEFAULT 1800 CHECK (stale_after_seconds BETWEEN interval_seconds AND 604800),
    refresh_on_change boolean NOT NULL DEFAULT true,
    near_reset_enabled boolean NOT NULL DEFAULT false,
    near_reset_lead_seconds integer NOT NULL DEFAULT 600 CHECK (near_reset_lead_seconds BETWEEN 60 AND 86400),
    near_reset_interval_seconds integer NOT NULL DEFAULT 60 CHECK (near_reset_interval_seconds BETWEEN 60 AND interval_seconds),
    stale_warnings_enabled boolean NOT NULL DEFAULT true,
    saved_at timestamptz
);
INSERT INTO monitoring_config(singleton) VALUES (true);

CREATE TABLE monitoring_worker (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    heartbeat_at timestamptz
);
INSERT INTO monitoring_worker(singleton) VALUES (true);

CREATE TABLE collection_runs (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    reason text NOT NULL CHECK (reason IN ('manual','scheduled')),
    actor_id text REFERENCES auth_users(id),
    actor_revision bigint CHECK (actor_revision > 0),
    actor_digest bytea,
    selected boolean NOT NULL,
    created_at timestamptz NOT NULL,
    CHECK ((reason='manual' AND actor_id IS NOT NULL AND actor_revision IS NOT NULL AND actor_digest IS NOT NULL AND octet_length(actor_digest)=32)
        OR (reason='scheduled' AND actor_id IS NULL AND actor_revision IS NULL AND actor_digest IS NULL AND NOT selected))
);

CREATE TABLE collection_jobs (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id bigint NOT NULL REFERENCES collection_runs(id),
    account_id text NOT NULL REFERENCES managed_accounts(id),
    source_id text NOT NULL REFERENCES connection_sources(id),
    connection_revision bigint NOT NULL CHECK (connection_revision > 0),
    account_revision bigint NOT NULL CHECK (account_revision > 0),
    policy_revision bigint NOT NULL CHECK (policy_revision > 0),
    source_mode text NOT NULL CHECK (source_mode IN ('active','passive')),
    state text NOT NULL CHECK (state IN ('queued','running','succeeded','failed','skipped','conflict','interrupted')),
    reason text NOT NULL,
    read_check jsonb,
    queued_at timestamptz NOT NULL,
    started_at timestamptz,
    finished_at timestamptz,
    retry_after timestamptz,
    UNIQUE(run_id,account_id)
);
CREATE UNIQUE INDEX collection_account_open ON collection_jobs(account_id) WHERE state IN ('queued','running');
CREATE INDEX collection_queue ON collection_jobs(id) WHERE state='queued';
CREATE INDEX collection_account_history ON collection_jobs(account_id,id DESC);
CREATE INDEX collection_run_jobs ON collection_jobs(run_id,id);

CREATE TABLE quota_observations (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    job_id bigint NOT NULL UNIQUE REFERENCES collection_jobs(id),
    account_id text NOT NULL REFERENCES managed_accounts(id),
    received_at timestamptz NOT NULL,
    snapshot jsonb NOT NULL
);
CREATE INDEX quota_account_history ON quota_observations(account_id,id DESC);
