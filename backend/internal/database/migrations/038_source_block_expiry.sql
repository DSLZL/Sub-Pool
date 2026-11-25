ALTER TABLE source_blocks
    ADD COLUMN reason text CONSTRAINT source_blocks_reason_check CHECK (char_length(reason) BETWEEN 1 AND 256),
    ADD COLUMN expires_at timestamptz CONSTRAINT source_blocks_expires_at_check CHECK (isfinite(expires_at));

CREATE INDEX source_blocks_expiry_idx ON source_blocks (expires_at, ip)
    WHERE blocked AND expires_at IS NOT NULL;
