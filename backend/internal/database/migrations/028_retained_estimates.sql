ALTER TABLE maintenance_plans ADD CONSTRAINT maintenance_retained_estimate_result CHECK (
    state NOT IN ('ready','queued','running','applied')
    OR snapshot->>'rules_version' <> 'retained-estimates-v2'
    OR COALESCE(
        snapshot #>> '{preview,financial,calculation_rules}' = 'gate-003-rules-v1'
        AND snapshot #>> '{preview,financial,currency}' = 'USD'
        AND snapshot #> '{preview,financial,scan_complete}' = 'true'::jsonb
        AND jsonb_typeof(snapshot #> '{preview,financial,results}') = 'array'
        AND jsonb_array_length(snapshot #> '{preview,financial,results}') BETWEEN 1 AND 2000
        AND snapshot #> '{preview,financial,records}' = to_jsonb(jsonb_array_length(snapshot #> '{preview,financial,results}')),
        false
    )
);
