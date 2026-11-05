CREATE TABLE auth_users (
    id text PRIMARY KEY,
    username text NOT NULL UNIQUE,
    role text NOT NULL CHECK (role IN ('admin', 'user')),
    enabled boolean NOT NULL DEFAULT true,
    password_hash text NOT NULL,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at timestamptz NOT NULL
);

CREATE TABLE auth_sessions (
    digest bytea PRIMARY KEY CHECK (octet_length(digest) = 32),
    user_id text NOT NULL REFERENCES auth_users(id),
    created_at timestamptz NOT NULL,
    idle_expires_at timestamptz NOT NULL,
    absolute_expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CHECK (created_at < idle_expires_at AND idle_expires_at <= absolute_expires_at)
);
CREATE INDEX auth_sessions_user ON auth_sessions(user_id);

CREATE TABLE auth_login_limits (
    bucket text PRIMARY KEY,
    window_start timestamptz NOT NULL,
    failures integer NOT NULL DEFAULT 0 CHECK (failures >= 0)
);

CREATE TABLE auth_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at timestamptz NOT NULL,
    action text NOT NULL,
    outcome text NOT NULL,
    user_id text REFERENCES auth_users(id),
    source_ip text NOT NULL
);
