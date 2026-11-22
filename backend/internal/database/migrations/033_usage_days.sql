ALTER TABLE monitoring_config
    ADD COLUMN usage_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN usage_interval_seconds integer NOT NULL DEFAULT 300 CHECK (usage_interval_seconds BETWEEN 60 AND 86400),
    ADD COLUMN usage_enabled_at timestamptz;
UPDATE monitoring_config SET usage_interval_seconds = interval_seconds;

ALTER TABLE usage_captures
    ALTER COLUMN actor_id DROP NOT NULL,
    ADD COLUMN collection_day date,
    ADD COLUMN staging boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT usage_capture_job_scope CHECK (
        (NOT staging OR collection_day IS NOT NULL) AND
        (actor_id IS NOT NULL OR collection_day IS NOT NULL)
    );

CREATE TABLE usage_day_jobs (
    source_id text NOT NULL REFERENCES connection_sources(id),
    account_id text NOT NULL REFERENCES managed_accounts(id),
    day date NOT NULL,
    connection_revision bigint NOT NULL CHECK (connection_revision > 0),
    fingerprint bytea NOT NULL,
    scheduled boolean NOT NULL,
    actor_id text REFERENCES auth_users(id),
    actor_revision bigint CHECK (actor_revision > 0),
    session_digest bytea,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'collecting', 'partial', 'waiting', 'complete', 'blocked')),
    reason text NOT NULL DEFAULT 'QUEUED',
    retry_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    work_capture_id bigint REFERENCES usage_captures(id),
    published_capture_id bigint REFERENCES usage_captures(id),
    checkpoint jsonb NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (source_id, account_id, day),
    CHECK ((scheduled AND actor_id IS NULL AND actor_revision IS NULL AND session_digest IS NULL) OR
           (NOT scheduled AND actor_id IS NOT NULL AND actor_revision IS NOT NULL AND octet_length(session_digest) = 32 AND session_digest IS NOT NULL)),
    CHECK (work_capture_id IS NULL OR work_capture_id IS DISTINCT FROM published_capture_id)
);

CREATE INDEX usage_days_due ON usage_day_jobs(source_id, retry_at, account_id, day)
    WHERE state NOT IN ('complete', 'blocked');
