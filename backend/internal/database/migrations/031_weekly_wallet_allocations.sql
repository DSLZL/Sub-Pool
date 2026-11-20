ALTER TABLE pools ADD COLUMN weekly_wallet jsonb;

CREATE TABLE weekly_wallet_allocations (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    pool_id text NOT NULL REFERENCES pools(id),
    account_id text NOT NULL REFERENCES managed_accounts(id),
    participant_id text NOT NULL REFERENCES participants(id),
    binding_id bigint NOT NULL REFERENCES participant_bindings(id),
    period_end timestamptz NOT NULL,
    phase text NOT NULL CHECK (phase IN ('advance','settlement')),
    application_id bigint UNIQUE REFERENCES wallet_application_requests(id),
    recorded_at timestamptz NOT NULL,
    snapshot jsonb NOT NULL
);
CREATE INDEX weekly_wallet_member_history ON weekly_wallet_allocations(pool_id,account_id,binding_id,id DESC);
