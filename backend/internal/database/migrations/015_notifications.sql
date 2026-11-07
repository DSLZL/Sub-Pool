CREATE TABLE notification_config (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    enabled boolean NOT NULL DEFAULT false,
    channel text NOT NULL DEFAULT 'smtp' CHECK (channel IN ('smtp','resend')),
    from_address text NOT NULL DEFAULT '',
    recipient_address text NOT NULL DEFAULT '',
    smtp_host text NOT NULL DEFAULT '',
    smtp_port integer NOT NULL DEFAULT 587 CHECK (smtp_port BETWEEN 1 AND 65535),
    smtp_tls text NOT NULL DEFAULT 'starttls' CHECK (smtp_tls IN ('tls','starttls','loopback_plain')),
    smtp_username text NOT NULL DEFAULT '',
    secret bytea,
    preferences jsonb NOT NULL DEFAULT '{"balance_low":false,"recommendation_changed":false,"estimate_changed":false,"collection_failed":false}',
    dedupe_seconds integer NOT NULL DEFAULT 3600 CHECK (dedupe_seconds BETWEEN 60 AND 86400),
    saved_at timestamptz
);
INSERT INTO notification_config(singleton) VALUES (true);

CREATE TABLE notifications (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_key text NOT NULL UNIQUE,
    kind text NOT NULL CHECK (kind IN ('test','collection_failed')),
    subject_type text NOT NULL CHECK (subject_type IN ('system','account')),
    subject_id text NOT NULL,
    participant_id text,
    source_id text REFERENCES connection_sources(id),
    connection_revision bigint,
    subject_revision bigint,
    config_revision bigint NOT NULL CHECK (config_revision > 0),
    channel text NOT NULL CHECK (channel IN ('smtp','resend')),
    from_address text NOT NULL,
    recipient_address text NOT NULL,
    subject text NOT NULL,
    body text NOT NULL,
    event_reason text NOT NULL,
    actor_id text REFERENCES auth_users(id),
    actor_revision bigint,
    actor_digest bytea,
    request_key text UNIQUE,
    created_at timestamptz NOT NULL,
    started_at timestamptz,
    finished_at timestamptz,
    state text NOT NULL CHECK (state IN ('queued','dispatching','accepted','failed','unknown','skipped')),
    reason text NOT NULL,
    provider_id text,
    CHECK ((kind='test' AND actor_id IS NOT NULL AND actor_revision IS NOT NULL AND octet_length(actor_digest)=32 AND request_key IS NOT NULL AND subject_type='system')
        OR (kind='collection_failed' AND actor_id IS NULL AND actor_revision IS NULL AND actor_digest IS NULL AND request_key IS NULL AND subject_type='account' AND source_id IS NOT NULL AND connection_revision IS NOT NULL AND subject_revision IS NOT NULL)),
    CHECK ((state IN ('queued','dispatching')) = (finished_at IS NULL)),
    CHECK (state <> 'accepted' OR started_at IS NOT NULL)
);
CREATE INDEX notifications_history ON notifications(created_at,id);
CREATE INDEX notifications_subject_history ON notifications(subject_type,subject_id,id);
CREATE INDEX notifications_queue ON notifications(id) WHERE state='queued';
CREATE INDEX notifications_dedupe ON notifications(kind,subject_type,subject_id,created_at) WHERE state IN ('queued','dispatching','accepted','unknown');
