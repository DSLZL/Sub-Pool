CREATE TABLE temporary_rounds (
    id text PRIMARY KEY CHECK (id ~ '^[A-Za-z0-9_-]{21}[AQgw]$'),
    actor_id text NOT NULL REFERENCES auth_users(id),
    actor_revision bigint NOT NULL CHECK (actor_revision > 0),
    request_id text NOT NULL,
    request_digest text NOT NULL CHECK (request_digest ~ '^[0-9a-f]{64}$'),
    started_at timestamptz NOT NULL,
    deadline timestamptz NOT NULL CHECK (deadline > started_at),
    snapshot jsonb NOT NULL CHECK (jsonb_typeof(snapshot) = 'object'),
    UNIQUE (actor_id, request_id)
);
CREATE TABLE temporary_exits (
    round_id text PRIMARY KEY REFERENCES temporary_rounds(id),
    exited_at timestamptz NOT NULL,
    reason text NOT NULL CHECK (reason IN ('deadline', 'period_changed', 'terminated')),
    actor_id text REFERENCES auth_users(id),
    recorded_at timestamptz NOT NULL,
    CHECK (exited_at <= recorded_at),
    CHECK ((reason = 'terminated') = (actor_id IS NOT NULL))
);
CREATE TABLE temporary_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    round_id text NOT NULL REFERENCES temporary_rounds(id),
    account_id text REFERENCES managed_accounts(id),
    kind text NOT NULL CHECK (kind IN ('settlement', 'rights', 'reminders')),
    request_key text NOT NULL UNIQUE,
    recorded_at timestamptz NOT NULL,
    snapshot jsonb NOT NULL CHECK (jsonb_typeof(snapshot) = 'object')
);
CREATE INDEX temporary_events_latest ON temporary_events(round_id, kind, account_id, id DESC);

ALTER TABLE notifications
    ADD COLUMN temporary_round_id text REFERENCES temporary_rounds(id),
    ADD COLUMN temporary_revision bigint,
    ADD COLUMN temporary_observation_id bigint REFERENCES quota_observations(id),
    ADD COLUMN temporary_expires_at timestamptz,
    DROP CONSTRAINT notifications_kind_check,
    DROP CONSTRAINT notifications_check;
ALTER TABLE notifications ADD CHECK (kind IN ('test','collection_failed','temporary_near','temporary_exhausted'));
ALTER TABLE notifications ADD CHECK (
    (kind='test' AND actor_id IS NOT NULL AND actor_revision IS NOT NULL AND octet_length(actor_digest)=32 AND request_key IS NOT NULL AND subject_type='system' AND temporary_round_id IS NULL)
    OR (kind='collection_failed' AND actor_id IS NULL AND actor_revision IS NULL AND actor_digest IS NULL AND request_key IS NULL AND subject_type='account' AND source_id IS NOT NULL AND connection_revision IS NOT NULL AND subject_revision IS NOT NULL AND temporary_round_id IS NULL)
    OR (kind IN ('temporary_near','temporary_exhausted') AND actor_id IS NULL AND actor_revision IS NULL AND actor_digest IS NULL AND request_key IS NULL AND subject_type='account' AND participant_id IS NOT NULL AND source_id IS NOT NULL AND connection_revision IS NOT NULL AND subject_revision IS NOT NULL AND temporary_round_id IS NOT NULL AND temporary_revision > 0 AND temporary_observation_id IS NOT NULL AND temporary_expires_at IS NOT NULL)
);
