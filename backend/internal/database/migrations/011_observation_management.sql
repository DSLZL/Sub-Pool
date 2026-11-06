ALTER TABLE quota_observations ADD CONSTRAINT quota_observation_account UNIQUE (id, account_id);

CREATE TABLE observation_management (
    account_id text PRIMARY KEY REFERENCES managed_accounts(id),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0)
);
CREATE TABLE observation_changes (
    id bigserial PRIMARY KEY,
    account_id text NOT NULL REFERENCES observation_management(account_id),
    revision bigint NOT NULL CHECK (revision > 1),
    action text NOT NULL CHECK (action IN ('exclude','restore','start','revoke')),
    observation_id bigint,
    start_observation_id bigint,
    end_observation_id bigint,
    window_kind text CHECK (window_kind IN ('short','seven_day')),
    reason text NOT NULL CHECK (length(reason) BETWEEN 1 AND 512),
    actor_id text NOT NULL REFERENCES auth_users(id),
    occurred_at timestamptz NOT NULL,
    UNIQUE (account_id, revision),
    FOREIGN KEY (observation_id, account_id) REFERENCES quota_observations(id, account_id),
    FOREIGN KEY (start_observation_id, account_id) REFERENCES quota_observations(id, account_id),
    FOREIGN KEY (end_observation_id, account_id) REFERENCES quota_observations(id, account_id),
    CHECK (
        (action IN ('exclude','restore') AND observation_id IS NOT NULL AND start_observation_id IS NULL AND end_observation_id IS NULL AND window_kind IS NULL)
        OR (action='start' AND observation_id IS NULL AND start_observation_id IS NOT NULL AND end_observation_id IS NOT NULL AND start_observation_id<>end_observation_id AND window_kind IS NOT NULL)
        OR (action='revoke' AND observation_id IS NULL AND start_observation_id IS NULL AND end_observation_id IS NULL AND window_kind IS NULL)
    )
);
CREATE INDEX observation_review_history ON observation_changes(observation_id, id DESC) WHERE observation_id IS NOT NULL;
CREATE INDEX observation_account_changes ON observation_changes(account_id, id);
CREATE INDEX observation_start_history ON observation_changes(account_id, id DESC) WHERE action IN ('start','revoke');
CREATE INDEX quota_receipt_history ON quota_observations(received_at, id);
