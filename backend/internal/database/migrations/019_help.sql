CREATE TABLE announcement_reads (
    user_id text NOT NULL REFERENCES auth_users(id),
    announcement_id text NOT NULL CHECK (announcement_id ~ '^[a-z0-9][a-z0-9-]{0,63}$'),
    content_sha256 text NOT NULL CHECK (content_sha256 ~ '^[0-9a-f]{64}$'),
    read_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (user_id, announcement_id, content_sha256)
);
