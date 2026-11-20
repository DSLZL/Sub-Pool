CREATE TABLE native_weekly_estimates (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id text NOT NULL REFERENCES managed_accounts(id),
    basis_key text NOT NULL CHECK (basis_key ~ '^[0-9a-f]{64}$'),
    retry_at timestamptz NOT NULL,
    work jsonb NOT NULL,
    result jsonb,
    UNIQUE (account_id, basis_key)
);
CREATE UNIQUE INDEX native_weekly_one_pending ON native_weekly_estimates(account_id) WHERE result IS NULL;
CREATE INDEX native_weekly_history ON native_weekly_estimates(account_id, id DESC) WHERE result IS NOT NULL;
