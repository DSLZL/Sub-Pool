ALTER TABLE notification_config
    ADD COLUMN native_source_id text REFERENCES connection_sources(id) ON DELETE SET NULL,
    ADD CHECK (financial_enabled OR native_source_id IS NULL);

CREATE TABLE native_wallet_financial_samples (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    participant_id text NOT NULL REFERENCES participants(id),
    binding_id bigint NOT NULL REFERENCES participant_bindings(id),
    source_id text NOT NULL REFERENCES connection_sources(id),
    connection_revision bigint NOT NULL CHECK (connection_revision > 0),
    participant_revision bigint NOT NULL CHECK (participant_revision > 0),
    policy_revision bigint NOT NULL CHECK (policy_revision > 0),
    sampled_at timestamptz NOT NULL,
    checked_at timestamptz NOT NULL CHECK (checked_at >= sampled_at),
    snapshot jsonb NOT NULL CHECK (jsonb_typeof(snapshot) = 'object')
);
CREATE INDEX native_wallet_financial_head ON native_wallet_financial_samples(participant_id,binding_id,id DESC);
