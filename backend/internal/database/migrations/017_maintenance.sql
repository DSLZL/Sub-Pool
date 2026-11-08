CREATE TABLE maintenance_plans (
    id bigserial PRIMARY KEY,
    account_id text NOT NULL REFERENCES managed_accounts(id),
    actor_id text NOT NULL REFERENCES auth_users(id),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK (expires_at > created_at),
    snapshot jsonb NOT NULL CHECK (jsonb_typeof(snapshot) = 'object'),
    state text NOT NULL CHECK (state IN ('ready','blocked','queued','running','applied','expired','failed')),
    reason text NOT NULL,
    queued_actor_id text REFERENCES auth_users(id),
    queued_actor_revision bigint CHECK (queued_actor_revision > 0),
    queued_digest bytea CHECK (octet_length(queued_digest) = 32),
    queued_at timestamptz,
    started_at timestamptz,
    finished_at timestamptz,
    CHECK ((queued_at IS NULL) = (queued_actor_id IS NULL)),
    CHECK ((queued_at IS NULL) = (queued_actor_revision IS NULL)),
    CHECK ((queued_at IS NULL) = (queued_digest IS NULL)),
    CHECK (state NOT IN ('queued','running','applied','failed') OR queued_at IS NOT NULL),
    CHECK (state NOT IN ('running','applied') OR started_at IS NOT NULL),
    CHECK ((state IN ('applied','failed','expired')) = (finished_at IS NOT NULL))
);
CREATE INDEX maintenance_account_history ON maintenance_plans(account_id, id);
CREATE INDEX maintenance_queue ON maintenance_plans(id) WHERE state = 'queued';
