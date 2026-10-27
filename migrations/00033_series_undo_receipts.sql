-- +goose Up
CREATE TABLE series_undo_receipts (
    id bigserial PRIMARY KEY,
    token_hash text NOT NULL,
    actor_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    event_id bigint NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    snapshot jsonb NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT series_undo_receipts_snapshot_array CHECK (jsonb_typeof(snapshot) = 'array'),
    CONSTRAINT series_undo_receipts_snapshot_bounded CHECK (octet_length(snapshot::text) <= 1048576),
    CONSTRAINT series_undo_receipts_expiry_order CHECK (expires_at > created_at),
    CONSTRAINT series_undo_receipts_used_order CHECK (used_at IS NULL OR used_at >= created_at)
);
CREATE UNIQUE INDEX idx_series_undo_receipts_token_hash ON series_undo_receipts(token_hash);
CREATE INDEX idx_series_undo_receipts_actor_id ON series_undo_receipts(actor_id);
CREATE INDEX idx_series_undo_receipts_expires_at ON series_undo_receipts(expires_at);

-- +goose Down
DROP TABLE series_undo_receipts;
