CREATE TABLE weekly_wallet_groups (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    participant_id text NOT NULL REFERENCES participants(id),
    binding_id bigint NOT NULL REFERENCES participant_bindings(id),
    pool_ids text[] NOT NULL CHECK (cardinality(pool_ids) > 0),
    phase text NOT NULL CHECK (phase IN ('advance','settlement')),
    period_end timestamptz NOT NULL,
    application_id bigint UNIQUE REFERENCES wallet_application_requests(id),
    recorded_at timestamptz NOT NULL,
    basis text NOT NULL CHECK (basis ~ '^[0-9a-f]{64}$'),
    snapshot jsonb NOT NULL
);
CREATE INDEX weekly_wallet_group_history ON weekly_wallet_groups(participant_id,binding_id,id DESC);
CREATE INDEX weekly_wallet_group_pools ON weekly_wallet_groups USING gin(pool_ids);
