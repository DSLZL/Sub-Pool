ALTER TABLE auth_users
    ADD COLUMN deleted_at timestamptz,
    ADD COLUMN read_features text[] NOT NULL DEFAULT '{}',
    ADD COLUMN account_ids text[] NOT NULL DEFAULT '{}',
    ADD COLUMN participant_ids text[] NOT NULL DEFAULT '{}',
    ADD CONSTRAINT auth_users_deleted CHECK (
        deleted_at IS NULL OR (role = 'user' AND NOT enabled AND password_hash = '')
    ),
    ADD CONSTRAINT auth_users_access_bounds CHECK (
        cardinality(read_features) <= 11 AND cardinality(account_ids) <= 500 AND cardinality(participant_ids) <= 500
        AND array_position(read_features, NULL) IS NULL
        AND array_position(account_ids, NULL) IS NULL
        AND array_position(participant_ids, NULL) IS NULL
    );

ALTER TABLE auth_events ADD COLUMN target_user_id text REFERENCES auth_users(id);
