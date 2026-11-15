ALTER TABLE notification_config ADD COLUMN financial_enabled boolean NOT NULL DEFAULT false;
ALTER TABLE notifications
    ADD COLUMN financial_evidence jsonb,
    DROP CONSTRAINT notifications_kind_check,
    DROP CONSTRAINT notifications_subject_type_check,
    DROP CONSTRAINT notifications_check;
ALTER TABLE notifications ADD CHECK (kind IN ('test','collection_failed','temporary_near','temporary_exhausted','balance_low','recommendation_changed','estimate_changed'));
ALTER TABLE notifications ADD CHECK (subject_type IN ('system','account','participant'));
ALTER TABLE notifications ADD CHECK (
    (kind='test' AND actor_id IS NOT NULL AND actor_revision IS NOT NULL AND octet_length(actor_digest)=32 AND request_key IS NOT NULL AND subject_type='system' AND temporary_round_id IS NULL)
    OR (kind='collection_failed' AND actor_id IS NULL AND actor_revision IS NULL AND actor_digest IS NULL AND request_key IS NULL AND subject_type='account' AND source_id IS NOT NULL AND connection_revision IS NOT NULL AND subject_revision IS NOT NULL AND temporary_round_id IS NULL)
    OR (kind IN ('temporary_near','temporary_exhausted') AND actor_id IS NULL AND actor_revision IS NULL AND actor_digest IS NULL AND request_key IS NULL AND subject_type='account' AND participant_id IS NOT NULL AND source_id IS NOT NULL AND connection_revision IS NOT NULL AND subject_revision IS NOT NULL AND temporary_round_id IS NOT NULL AND temporary_revision > 0 AND temporary_observation_id IS NOT NULL AND temporary_expires_at IS NOT NULL)
    OR (kind IN ('balance_low','recommendation_changed','estimate_changed') AND actor_id IS NULL AND actor_revision IS NULL AND actor_digest IS NULL AND request_key IS NULL AND source_id IS NOT NULL AND connection_revision IS NOT NULL AND connection_revision > 0 AND subject_revision IS NOT NULL AND subject_revision > 0 AND temporary_round_id IS NULL
        AND ((kind='estimate_changed' AND subject_type='account' AND participant_id IS NULL) OR (kind IN ('balance_low','recommendation_changed') AND subject_type='participant' AND participant_id IS NOT NULL AND participant_id=subject_id)))
);
ALTER TABLE notifications ADD CHECK ((kind IN ('balance_low','recommendation_changed','estimate_changed')) = (financial_evidence IS NOT NULL));
ALTER TABLE notifications ADD CHECK (financial_evidence IS NULL OR jsonb_typeof(financial_evidence)='object');
