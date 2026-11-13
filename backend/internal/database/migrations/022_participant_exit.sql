ALTER TABLE participant_events ADD COLUMN details jsonb;
ALTER TABLE participant_events DROP CONSTRAINT participant_events_action_check;
ALTER TABLE participant_events ADD CONSTRAINT participant_events_action_check
    CHECK (action IN ('participant.create','participant.update','participant.delete','participant.exit'));
CREATE UNIQUE INDEX participant_exit_revision ON participant_events(participant_id, revision)
    WHERE action='participant.exit';
ALTER TABLE pool_events DROP CONSTRAINT pool_events_action_check;
ALTER TABLE pool_events ADD CONSTRAINT pool_events_action_check
    CHECK (action IN ('pool.create','pool.update','pool.disable','pool.participant.exit'));
