ALTER TABLE connection_config ADD COLUMN reverification_required boolean NOT NULL DEFAULT false;

CREATE TABLE backup_archives (
    id text PRIMARY KEY CHECK (id ~ '^[A-Za-z0-9_-]{21}[AQgw]$'),
    kind text NOT NULL CHECK (kind IN ('export','pre_restore')),
    created_at timestamptz NOT NULL,
    actor_id text NOT NULL,
    actor_username text NOT NULL,
    include_secrets boolean NOT NULL,
    source_cutoff timestamptz NOT NULL,
    file_sha256 text NOT NULL CHECK (file_sha256 ~ '^[a-f0-9]{64}$'),
    file text NOT NULL CHECK (octet_length(file) BETWEEN 1 AND 12582912),
    manifest jsonb NOT NULL CHECK (jsonb_typeof(manifest) = 'object')
);
CREATE INDEX backup_archives_history ON backup_archives(created_at DESC,id DESC);

CREATE TABLE restore_plans (
    id text PRIMARY KEY CHECK (id ~ '^[A-Za-z0-9_-]{21}[AQgw]$'),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK (expires_at > created_at),
    actor_id text NOT NULL,
    actor_username text NOT NULL,
    file_sha256 text NOT NULL CHECK (file_sha256 ~ '^[a-f0-9]{64}$'),
    file text NOT NULL CHECK (octet_length(file) BETWEEN 1 AND 12582912),
    manifest jsonb NOT NULL CHECK (jsonb_typeof(manifest) = 'object'),
    target_basis text NOT NULL CHECK (target_basis ~ '^[a-f0-9]{64}$'),
    before_archive_id text REFERENCES backup_archives(id),
    state text NOT NULL CHECK (state IN ('ready','blocked','applying','applied','expired','failed')),
    reason text NOT NULL,
    blockers text[] NOT NULL,
    started_at timestamptz,
    finished_at timestamptz,
    reviewed_at timestamptz,
    reviewed_by text,
    CHECK ((reviewed_at IS NULL) = (reviewed_by IS NULL)),
    CHECK (state NOT IN ('ready','applying','applied') OR before_archive_id IS NOT NULL),
    CHECK ((state IN ('applied','expired','failed')) = (finished_at IS NOT NULL)),
    CHECK (state NOT IN ('applying','applied') OR started_at IS NOT NULL)
);
