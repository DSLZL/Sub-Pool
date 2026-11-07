CREATE TABLE auth_api_keys (
    id text PRIMARY KEY,
    user_id text NOT NULL REFERENCES auth_users(id),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80 AND octet_length(name) <= 256),
    mode text NOT NULL CHECK (mode IN ('read', 'management')),
    digest bytea NOT NULL UNIQUE CHECK (octet_length(digest) = 32),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at timestamptz NOT NULL,
    rotated_at timestamptz,
    last_used_at timestamptz,
    revoked_at timestamptz,
    CHECK (rotated_at IS NULL OR rotated_at >= created_at),
    CHECK (last_used_at IS NULL OR last_used_at >= created_at),
    CHECK (revoked_at IS NULL OR revoked_at >= created_at)
);
CREATE INDEX auth_api_keys_owner ON auth_api_keys(user_id, id);
