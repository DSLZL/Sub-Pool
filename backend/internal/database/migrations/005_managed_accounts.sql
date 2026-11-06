CREATE TABLE managed_accounts (
    id text PRIMARY KEY,
    source_id text NOT NULL REFERENCES connection_sources(id),
    upstream_id bigint NOT NULL CHECK (upstream_id > 0),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    name text NOT NULL,
    monitoring boolean NOT NULL,
    source_mode text NOT NULL CHECK (source_mode IN ('active', 'passive')),
    identity_state text NOT NULL CHECK (identity_state IN ('unverified', 'verified', 'shadow', 'unsupported', 'conflict')),
    resource_fingerprint bytea,
    snapshot jsonb NOT NULL,
    read_check jsonb NOT NULL,
    last_attempt_at timestamptz NOT NULL,
    observed_at timestamptz NOT NULL,
    observed_connection_revision bigint NOT NULL CHECK (observed_connection_revision > 0),
    created_at timestamptz NOT NULL,
    saved_at timestamptz NOT NULL,
    UNIQUE (source_id, upstream_id),
    CHECK (identity_state <> 'verified' OR resource_fingerprint IS NOT NULL),
    CHECK (resource_fingerprint IS NULL OR octet_length(resource_fingerprint) = 32)
);
CREATE UNIQUE INDEX managed_account_resource ON managed_accounts(source_id, resource_fingerprint) WHERE resource_fingerprint IS NOT NULL;

CREATE TABLE account_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at timestamptz NOT NULL,
    actor_id text NOT NULL REFERENCES auth_users(id),
    account_id text NOT NULL REFERENCES managed_accounts(id),
    revision bigint NOT NULL,
    action text NOT NULL CHECK (action IN ('account.adopt', 'account.update', 'account.refresh', 'account.models.read'))
);
