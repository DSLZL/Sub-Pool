CREATE TABLE pricing_changes (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    actor_id text NOT NULL REFERENCES auth_users(id),
    request_id text NOT NULL,
    request_digest text NOT NULL,
    source_id text NOT NULL REFERENCES connection_sources(id),
    connection_revision bigint NOT NULL CHECK (connection_revision > 0),
    group_id bigint NOT NULL CHECK (group_id > 0),
    group_name text NOT NULL,
    changes jsonb NOT NULL CHECK (jsonb_typeof(changes) = 'array'),
    native_before jsonb NOT NULL CHECK (jsonb_typeof(native_before) = 'object'),
    native_target jsonb NOT NULL CHECK (jsonb_typeof(native_target) = 'object'),
    native_after jsonb,
    state text NOT NULL CHECK (state IN ('applying','applied','reverting','reverted','rejected','conflict','needs_review','reviewed')),
    reason text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (actor_id, request_id)
);
CREATE UNIQUE INDEX pricing_one_owned_change ON pricing_changes(source_id, group_id)
    WHERE state IN ('applying','applied','reverting','needs_review');
CREATE TABLE pricing_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    change_id bigint NOT NULL REFERENCES pricing_changes(id),
    occurred_at timestamptz NOT NULL,
    snapshot jsonb NOT NULL CHECK (jsonb_typeof(snapshot) = 'object')
);
