CREATE TABLE participants (
    id text PRIMARY KEY,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    name text NOT NULL,
    contact text NOT NULL,
    notes text NOT NULL,
    enabled boolean NOT NULL,
    created_at timestamptz NOT NULL,
    saved_at timestamptz NOT NULL,
    deleted_at timestamptz
);

CREATE TABLE participant_bindings (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    participant_id text NOT NULL REFERENCES participants(id),
    source_id text NOT NULL,
    upstream_user_id bigint NOT NULL CHECK (upstream_user_id > 0),
    snapshot jsonb NOT NULL,
    started_at timestamptz NOT NULL,
    ended_at timestamptz,
    CHECK (ended_at IS NULL OR ended_at >= started_at)
);
CREATE UNIQUE INDEX participant_active_binding ON participant_bindings(participant_id) WHERE ended_at IS NULL;
CREATE UNIQUE INDEX participant_native_owner ON participant_bindings(source_id, upstream_user_id) WHERE ended_at IS NULL;
CREATE INDEX participant_binding_history ON participant_bindings(source_id, upstream_user_id, started_at);

CREATE TABLE participant_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at timestamptz NOT NULL,
    actor_id text NOT NULL REFERENCES auth_users(id),
    participant_id text NOT NULL REFERENCES participants(id),
    revision bigint NOT NULL,
    action text NOT NULL CHECK (action IN ('participant.create','participant.update','participant.delete'))
);

CREATE TABLE usage_attributions (
    capture_id bigint NOT NULL,
    upstream_id bigint NOT NULL,
    participant_id text REFERENCES participants(id),
    binding_id bigint REFERENCES participant_bindings(id),
    participant_name text,
    basis text NOT NULL CHECK (basis IN ('local_binding_at_log_time','mapping_not_recorded','missing_user_id','outside_recorded_binding')),
    PRIMARY KEY (capture_id, upstream_id),
    FOREIGN KEY (capture_id, upstream_id) REFERENCES usage_capture_facts(capture_id, upstream_id),
    CHECK ((basis='local_binding_at_log_time' AND participant_id IS NOT NULL AND binding_id IS NOT NULL AND participant_name IS NOT NULL)
        OR (basis<>'local_binding_at_log_time' AND participant_id IS NULL AND binding_id IS NULL AND participant_name IS NULL))
);
CREATE INDEX participant_usage_history ON usage_attributions(participant_id) WHERE participant_id IS NOT NULL;
INSERT INTO usage_attributions(capture_id,upstream_id,basis)
SELECT capture_id,upstream_id,'mapping_not_recorded' FROM usage_capture_facts;
