CREATE TABLE wallet_automation (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    enabled boolean NOT NULL DEFAULT false,
    actor_id text REFERENCES auth_users(id),
    actor_revision bigint CHECK (actor_revision > 0),
    source_id text REFERENCES connection_sources(id),
    connection_revision bigint CHECK (connection_revision > 0),
    simulation_epoch text CHECK (simulation_epoch ~ '^[A-Za-z0-9_-]{21}[AQgw]$'),
    updated_at timestamptz,
    CHECK (NOT enabled OR (actor_id IS NOT NULL AND actor_revision IS NOT NULL
        AND source_id IS NOT NULL AND connection_revision IS NOT NULL
        AND simulation_epoch IS NOT NULL AND updated_at IS NOT NULL))
);
INSERT INTO wallet_automation(singleton) VALUES (true);

CREATE UNIQUE INDEX wallet_automatic_recommendation_once
    ON wallet_application_requests ((snapshot #>> '{details,input,recommendation_id}'))
    WHERE snapshot #>> '{details,automation,control_revision}' IS NOT NULL;
