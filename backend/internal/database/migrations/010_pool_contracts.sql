CREATE TABLE pools (
    id text PRIMARY KEY,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    name text NOT NULL,
    enabled boolean NOT NULL,
    created_at timestamptz NOT NULL,
    saved_at timestamptz NOT NULL
);
CREATE TABLE pool_contracts (
    id bigserial PRIMARY KEY,
    pool_id text NOT NULL REFERENCES pools(id),
    revision bigint NOT NULL CHECK (revision > 0),
    name text NOT NULL,
    enabled boolean NOT NULL,
    source_id text NOT NULL,
    connection_revision bigint NOT NULL CHECK (connection_revision > 0),
    effective_at timestamptz NOT NULL,
    ended_at timestamptz,
    CHECK (ended_at IS NULL OR ended_at >= effective_at),
    UNIQUE (pool_id, revision)
);
CREATE UNIQUE INDEX pool_contract_current ON pool_contracts(pool_id) WHERE ended_at IS NULL;
CREATE TABLE pool_contract_accounts (
    contract_id bigint NOT NULL REFERENCES pool_contracts(id),
    account_id text NOT NULL REFERENCES managed_accounts(id),
    revision bigint NOT NULL CHECK (revision > 0),
    name text NOT NULL,
    source_id text NOT NULL,
    upstream_id bigint NOT NULL CHECK (upstream_id > 0),
    resource_fingerprint bytea NOT NULL CHECK (octet_length(resource_fingerprint) = 32),
    current boolean NOT NULL,
    PRIMARY KEY (contract_id, account_id)
);
CREATE UNIQUE INDEX pool_account_current ON pool_contract_accounts(account_id) WHERE current;
CREATE TABLE pool_contract_participants (
    contract_id bigint NOT NULL REFERENCES pool_contracts(id),
    participant_id text NOT NULL REFERENCES participants(id),
    revision bigint NOT NULL CHECK (revision > 0),
    binding_id bigint NOT NULL REFERENCES participant_bindings(id),
    name text NOT NULL,
    share_units integer NOT NULL CHECK (share_units BETWEEN 0 AND 100000000),
    PRIMARY KEY (contract_id, participant_id)
);
CREATE INDEX pool_participant_history ON pool_contract_participants(participant_id, contract_id);
CREATE TABLE pool_events (
    id bigserial PRIMARY KEY,
    occurred_at timestamptz NOT NULL,
    actor_id text NOT NULL REFERENCES auth_users(id),
    pool_id text NOT NULL REFERENCES pools(id),
    contract_id bigint NOT NULL REFERENCES pool_contracts(id),
    action text NOT NULL CHECK (action IN ('pool.create','pool.update','pool.disable'))
);
ALTER TABLE usage_attributions ADD COLUMN pool_contract_id bigint REFERENCES pool_contracts(id);
ALTER TABLE usage_attributions ADD COLUMN pool_basis text NOT NULL DEFAULT 'mapping_not_recorded'
    CHECK (pool_basis IN ('mapping_not_recorded','recorded_contract','unassigned_participant','no_recorded_contract','contract_member_missing'));
ALTER TABLE usage_attributions ADD CONSTRAINT usage_pool_evidence CHECK (
    (pool_basis = 'recorded_contract' AND pool_contract_id IS NOT NULL AND participant_id IS NOT NULL)
    OR (pool_basis <> 'recorded_contract' AND pool_contract_id IS NULL)
);
