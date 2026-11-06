CREATE TABLE usage_captures (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id text NOT NULL REFERENCES managed_accounts(id),
    actor_id text NOT NULL REFERENCES auth_users(id),
    captured_at timestamptz NOT NULL,
    snapshot jsonb NOT NULL
);
CREATE INDEX usage_capture_account ON usage_captures(account_id, id);

CREATE TABLE usage_capture_facts (
    capture_id bigint NOT NULL REFERENCES usage_captures(id),
    upstream_id bigint NOT NULL CHECK (upstream_id > 0),
    fact jsonb NOT NULL,
    PRIMARY KEY (capture_id, upstream_id)
);

CREATE TABLE usage_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at timestamptz NOT NULL,
    actor_id text NOT NULL REFERENCES auth_users(id),
    capture_id bigint NOT NULL REFERENCES usage_captures(id),
    action text NOT NULL CHECK (action = 'usage.capture')
);
