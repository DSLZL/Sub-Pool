CREATE TABLE estimate_config (
    account_id text PRIMARY KEY REFERENCES managed_accounts(id),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    configuration jsonb,
    saved_at timestamptz,
    CHECK ((configuration IS NULL) = (saved_at IS NULL))
);
CREATE TABLE estimate_results (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id text NOT NULL REFERENCES estimate_config(account_id),
    generated_at timestamptz NOT NULL,
    snapshot jsonb NOT NULL,
    UNIQUE (id, account_id)
);
CREATE INDEX estimate_account_history ON estimate_results(account_id, id);
