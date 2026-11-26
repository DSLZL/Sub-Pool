CREATE TABLE weekly_wallet_issuances (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    binding_id bigint NOT NULL REFERENCES participant_bindings(id),
    application_id bigint UNIQUE REFERENCES wallet_application_requests(id),
    recorded_at timestamptz NOT NULL,
    basis text NOT NULL CHECK (basis ~ '^[0-9a-f]{64}$'),
    snapshot jsonb NOT NULL CHECK (jsonb_typeof(snapshot) = 'object')
);
CREATE INDEX weekly_wallet_issuance_binding ON weekly_wallet_issuances(binding_id, id DESC);

CREATE TABLE weekly_wallet_issuance_phases (
    source_id text NOT NULL,
    upstream_user_id bigint NOT NULL CHECK (upstream_user_id > 0),
    pool_id text NOT NULL REFERENCES pools(id),
    account_id text NOT NULL REFERENCES managed_accounts(id),
    period_end timestamptz NOT NULL,
    phase text NOT NULL CHECK (phase IN ('advance', 'settlement')),
    issuance_id bigint NOT NULL REFERENCES weekly_wallet_issuances(id),
    PRIMARY KEY (source_id, upstream_user_id, pool_id, account_id, period_end, phase)
);
CREATE INDEX weekly_wallet_issuance_pool ON weekly_wallet_issuance_phases(pool_id, issuance_id);

ALTER TABLE wallet_application_results DROP CONSTRAINT wallet_application_results_check;
ALTER TABLE wallet_application_results ADD CONSTRAINT wallet_application_results_check CHECK ((
    ((snapshot->>'unit' = 'simulated_wallet_usd' AND snapshot->'simulation'->>'environment' = 'simulated')
    OR (snapshot->>'unit' = 'native_wallet'
        AND jsonb_typeof(snapshot #> '{details,input,native_delta}') = 'string'
        AND jsonb_typeof(snapshot #> '{details,native_adjustment}') = 'object'
        AND (snapshot #> '{details,native_adjustment,weekly_issuance}' IS NULL
            OR (snapshot #> '{details,native_adjustment,weekly_issuance}' = 'true'
                AND snapshot #>> '{details,input,native_delta}' ~ '^[0-9]{1,12}(\.[0-9]{1,8})?$'
                AND (snapshot #>> '{details,input,native_delta}')::numeric > 0))
        AND (snapshot->>'state' <> 'applied'
            OR (snapshot #>> '{details,native_adjustment,ledger_id}' ~ '^[1-9][0-9]*$'
                AND (jsonb_typeof(snapshot #> '{details,native_adjustment,balance_after}') = 'string'
                    OR snapshot #> '{details,native_adjustment,weekly_issuance}' = 'true')))))
    AND ((snapshot->>'state' = 'applied' AND snapshot->>'dispatch_state' = 'confirmed' AND resolved)
        OR (snapshot->>'state' = 'failed' AND snapshot->>'dispatch_state' = 'confirmed_failed' AND resolved)
        OR (snapshot->>'state' = 'rejected' AND snapshot->>'dispatch_state' = 'not_dispatched' AND resolved)
        OR (snapshot->>'state' = 'unknown' AND snapshot->>'dispatch_state' = 'unknown' AND NOT resolved))
) IS TRUE);

CREATE UNIQUE INDEX weekly_wallet_issuance_receipt ON wallet_application_results (
    (snapshot #>> '{details,input,source_id}'),
    (snapshot #>> '{details,upstream_user_id}'),
    (snapshot #>> '{details,native_adjustment,ledger_id}')
) WHERE snapshot->>'state' = 'applied' AND snapshot #> '{details,native_adjustment,weekly_issuance}' = 'true';
