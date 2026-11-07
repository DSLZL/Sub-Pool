CREATE TABLE wallet_reads (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    participant_id text NOT NULL REFERENCES participants(id),
    binding_id bigint NOT NULL REFERENCES participant_bindings(id),
    source_id text NOT NULL,
    connection_revision bigint NOT NULL CHECK (connection_revision > 0),
    attempted_at timestamptz NOT NULL,
    snapshot jsonb NOT NULL
);
CREATE INDEX wallet_reads_history ON wallet_reads(participant_id, id);

CREATE TABLE wallet_recommendations (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    participant_id text NOT NULL REFERENCES participants(id),
    binding_id bigint NOT NULL REFERENCES participant_bindings(id),
    generated_at timestamptz NOT NULL,
    snapshot jsonb NOT NULL
);
CREATE INDEX wallet_recommendations_history ON wallet_recommendations(participant_id, id);
