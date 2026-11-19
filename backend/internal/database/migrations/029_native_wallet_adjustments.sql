ALTER TABLE wallet_application_results DROP CONSTRAINT wallet_application_results_check;

ALTER TABLE wallet_application_results ADD CONSTRAINT wallet_application_results_check CHECK ((
    ((snapshot->>'unit' = 'simulated_wallet_usd' AND snapshot->'simulation'->>'environment' = 'simulated')
    OR (snapshot->>'unit' = 'native_wallet'
        AND jsonb_typeof(snapshot #> '{details,input,native_delta}') = 'string'
        AND jsonb_typeof(snapshot #> '{details,native_adjustment}') = 'object'
        AND (snapshot->>'state' <> 'applied'
            OR (snapshot #>> '{details,native_adjustment,ledger_id}' ~ '^[1-9][0-9]*$'
                AND jsonb_typeof(snapshot #> '{details,native_adjustment,balance_after}') = 'string'))))
    AND ((snapshot->>'state' = 'applied' AND snapshot->>'dispatch_state' = 'confirmed' AND resolved)
        OR (snapshot->>'state' = 'failed' AND snapshot->>'dispatch_state' = 'confirmed_failed' AND resolved)
        OR (snapshot->>'state' = 'rejected' AND snapshot->>'dispatch_state' = 'not_dispatched' AND resolved)
        OR (snapshot->>'state' = 'unknown' AND snapshot->>'dispatch_state' = 'unknown' AND NOT resolved))
) IS TRUE);
