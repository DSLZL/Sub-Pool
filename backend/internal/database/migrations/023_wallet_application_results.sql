CREATE TABLE wallet_application_results (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    application_id bigint NOT NULL REFERENCES wallet_application_requests(id),
    recorded_at timestamptz NOT NULL,
    resolved boolean NOT NULL,
    snapshot jsonb NOT NULL,
    CHECK ((snapshot->>'unit' = 'simulated_wallet_usd'
        AND snapshot->'simulation'->>'environment' = 'simulated'
        AND ((snapshot->>'state' = 'applied' AND snapshot->>'dispatch_state' = 'confirmed' AND resolved)
            OR (snapshot->>'state' = 'failed' AND snapshot->>'dispatch_state' = 'confirmed_failed' AND resolved)
            OR (snapshot->>'state' = 'rejected' AND snapshot->>'dispatch_state' = 'not_dispatched' AND resolved)
            OR (snapshot->>'state' = 'unknown' AND snapshot->>'dispatch_state' = 'unknown' AND NOT resolved))) IS TRUE)
);
CREATE INDEX wallet_application_result_history ON wallet_application_results(application_id, id DESC);
