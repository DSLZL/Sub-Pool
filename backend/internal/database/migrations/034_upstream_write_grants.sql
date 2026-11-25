CREATE TABLE upstream_write_grants (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_id text NOT NULL REFERENCES connection_sources(id),
    capability text NOT NULL CHECK (capability IN ('pricing', 'scheduling', 'wallet')),
    granted_by text NOT NULL CHECK (granted_by ~ '^[A-Za-z0-9_-]{21}[AQgw]$'),
    granted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    confirmation_sha256 text NOT NULL CHECK (confirmation_sha256 ~ '^[0-9a-f]{64}$'),
    revoked_by text CHECK (revoked_by ~ '^[A-Za-z0-9_-]{21}[AQgw]$'),
    revoked_at timestamptz,
    CHECK ((revoked_by IS NULL) = (revoked_at IS NULL)),
    CHECK (revoked_at >= granted_at)
);

CREATE UNIQUE INDEX upstream_write_grants_active
    ON upstream_write_grants (source_id, capability) WHERE revoked_at IS NULL;
