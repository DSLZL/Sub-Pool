ALTER TABLE auth_users DROP CONSTRAINT auth_users_access_bounds;
ALTER TABLE auth_users ADD CONSTRAINT auth_users_access_bounds CHECK (
    cardinality(read_features) <= 12 AND cardinality(account_ids) <= 500 AND cardinality(participant_ids) <= 500
    AND array_position(read_features, NULL) IS NULL AND array_position(account_ids, NULL) IS NULL
    AND array_position(participant_ids, NULL) IS NULL
);
ALTER TABLE auth_events ADD COLUMN peer_ip text,
    ADD COLUMN source_kind text NOT NULL DEFAULT 'legacy' CHECK (source_kind IN ('legacy','peer','trusted_forwarded','unknown','local','session'));
CREATE INDEX auth_login_events_time ON auth_events(occurred_at,id) WHERE action='login';

CREATE TABLE security_config (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    network_diagnostics_enabled boolean NOT NULL DEFAULT false
);
INSERT INTO security_config(singleton) VALUES (true);

CREATE TABLE source_blocks (
    ip inet PRIMARY KEY CHECK (masklen(ip) = CASE family(ip) WHEN 4 THEN 32 ELSE 128 END),
    revision bigint NOT NULL CHECK (revision > 0),
    blocked boolean NOT NULL,
    saved_at timestamptz NOT NULL,
    actor_id text NOT NULL REFERENCES auth_users(id)
);

CREATE TABLE management_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    operation_id text NOT NULL,
    occurred_at timestamptz NOT NULL,
    actor_id text REFERENCES auth_users(id),
    task_id text,
    action text NOT NULL,
    outcome text NOT NULL CHECK (outcome IN ('pending','succeeded','failed','unknown','denied','conflict')),
    target_type text NOT NULL,
    target_id text NOT NULL,
    target_revision bigint CHECK (target_revision > 0),
    source_id text REFERENCES connection_sources(id),
    CHECK ((actor_id IS NULL) <> (task_id IS NULL))
);
CREATE INDEX management_events_operation ON management_events(operation_id,id);
CREATE INDEX management_events_time ON management_events(occurred_at,id);

INSERT INTO management_events(operation_id,occurred_at,actor_id,action,outcome,target_type,target_id)
SELECT 'legacy:auth:'||id,occurred_at,user_id,action,
    CASE outcome WHEN 'success' THEN 'succeeded' ELSE 'failed' END,'user',COALESCE(target_user_id,user_id)
FROM auth_events WHERE action <> 'login' AND user_id IS NOT NULL;
INSERT INTO management_events(operation_id,occurred_at,actor_id,action,outcome,target_type,target_id,target_revision,source_id)
SELECT 'legacy:system:'||id,occurred_at,actor_id,action,'succeeded',
    CASE WHEN action='settings.update' THEN 'settings' ELSE 'connection' END,
    COALESCE(source_id,'global'),revision,source_id FROM system_events;
INSERT INTO management_events(operation_id,occurred_at,actor_id,action,outcome,target_type,target_id,target_revision)
SELECT 'legacy:account:'||id,occurred_at,actor_id,action,'succeeded','account',account_id,revision FROM account_events;
