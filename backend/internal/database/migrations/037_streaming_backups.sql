CREATE TABLE backup_blobs (
    id text PRIMARY KEY CHECK (id ~ '^[A-Za-z0-9_-]{21}[AQgw]$')
);

CREATE TABLE backup_blob_chunks (
    blob_id text NOT NULL REFERENCES backup_blobs(id) ON DELETE CASCADE,
    part integer NOT NULL CHECK (part >= 0),
    body bytea NOT NULL CHECK (octet_length(body) BETWEEN 1 AND 1048576),
    PRIMARY KEY (blob_id, part)
);

ALTER TABLE backup_archives
    ALTER COLUMN file DROP NOT NULL,
    ADD COLUMN blob_id text REFERENCES backup_blobs(id),
    ADD COLUMN file_bytes bigint,
    ADD COLUMN snapshot_basis text,
    ADD CONSTRAINT backup_archives_payload CHECK (
        (file IS NOT NULL AND blob_id IS NULL AND file_bytes IS NULL AND snapshot_basis IS NULL)
        OR (file IS NULL AND blob_id IS NOT NULL AND file_bytes IS NOT NULL AND file_bytes > 0
            AND (kind = 'pre_restore') = (snapshot_basis IS NOT NULL)
            AND (snapshot_basis IS NULL OR snapshot_basis ~ '^[a-f0-9]{64}$'))
    );

ALTER TABLE restore_plans
    ALTER COLUMN file DROP NOT NULL,
    ADD COLUMN blob_id text REFERENCES backup_blobs(id),
    ADD COLUMN file_bytes bigint,
    ADD COLUMN cleared_tables jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(cleared_tables) = 'array'),
    ADD COLUMN before_download_proof text CHECK (before_download_proof ~ '^[a-f0-9]{64}$'),
    ADD CONSTRAINT restore_plans_payload CHECK (
        (file IS NOT NULL AND blob_id IS NULL AND file_bytes IS NULL)
        OR (file IS NULL AND blob_id IS NOT NULL AND file_bytes IS NOT NULL AND file_bytes > 0)
    );
