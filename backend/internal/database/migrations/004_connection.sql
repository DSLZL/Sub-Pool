CREATE TABLE connection_sources (
    id text PRIMARY KEY,
    base_url text NOT NULL,
    created_at timestamptz NOT NULL
);

CREATE TABLE connection_config (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    source_id text REFERENCES connection_sources(id),
    credential_kind text CHECK (credential_kind IN ('api_key', 'bearer')),
    secret bytea,
    saved_at timestamptz,
    diagnostic jsonb,
    CHECK ((secret IS NULL) = (credential_kind IS NULL)),
    CHECK (source_id IS NOT NULL OR secret IS NULL)
);
INSERT INTO connection_config(singleton) VALUES (true);

CREATE TABLE system_settings (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    timezone text NOT NULL DEFAULT 'UTC',
    saved_at timestamptz
);
INSERT INTO system_settings(singleton) VALUES (true);

CREATE TABLE system_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at timestamptz NOT NULL,
    actor_id text NOT NULL REFERENCES auth_users(id),
    source_id text REFERENCES connection_sources(id),
    revision bigint NOT NULL,
    action text NOT NULL CHECK (action IN ('connection.save', 'connection.test', 'connection.discover', 'settings.update'))
);
