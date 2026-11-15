INSERT INTO management_events(operation_id,occurred_at,task_id,action,outcome,target_type,target_id)
SELECT 'temporary-settlement:'||e.id,e.recorded_at,'temporary:'||e.round_id,'temporary.settlement',
    CASE
        WHEN e.snapshot->>'state'='pending_evidence' THEN 'pending'
        WHEN e.snapshot->>'state' IN ('settled','no_carry','no_borrowing','cancelled') THEN 'succeeded'
        ELSE 'unknown'
    END,
    'account',e.account_id
FROM temporary_events e
WHERE e.kind='settlement'
    AND NOT EXISTS (SELECT 1 FROM management_events m
        WHERE m.operation_id='temporary-settlement:'||e.id AND m.action='temporary.settlement');
