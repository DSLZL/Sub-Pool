CREATE TABLE wallet_application_requests (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_id text NOT NULL REFERENCES auth_users(id),
    request_id text NOT NULL CHECK (request_id ~ '^[A-Za-z0-9_-]{21}[AQgw]$'),
    request_digest text NOT NULL CHECK (request_digest ~ '^[a-f0-9]{64}$'),
    participant_id text NOT NULL REFERENCES participants(id),
    binding_id bigint NOT NULL REFERENCES participant_bindings(id),
    attempted_at timestamptz NOT NULL,
    snapshot jsonb NOT NULL,
    UNIQUE (actor_id, request_id)
);
CREATE INDEX wallet_application_participant_history ON wallet_application_requests(participant_id, id);
