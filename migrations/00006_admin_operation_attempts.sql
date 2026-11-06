-- +goose Up
-- M7.4 T07B-1a: dormant receipts for lost acknowledgments. This DEC-138
-- additive step stores only a random nonce and timestamp, with no personal
-- data or foreign keys. No production caller or cleanup runs in this slice.
CREATE TABLE admin_operation_attempts (
    attempt_id bytea PRIMARY KEY CHECK (octet_length(attempt_id) = 16),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX admin_operation_attempts_created_at ON admin_operation_attempts (created_at);

-- +goose Down
-- Drops only transient receipts, never a domain operation or its audit rows.
DROP TABLE admin_operation_attempts;
