-- +goose Up
-- Room feeds (M2.2b, 8.2): an admin-issued secret link per room that
-- exports its confirmed bookings as ICS. Only the SHA-256 of the token is
-- stored. A new link revokes the old one; revoked rows stay as history.
CREATE TABLE room_feed_tokens (
    id bigserial PRIMARY KEY,
    room_id bigint NOT NULL REFERENCES rooms(id),
    token_hash text NOT NULL,
    created_by bigint REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz
);
CREATE UNIQUE INDEX idx_room_feed_tokens_token_hash ON room_feed_tokens (token_hash);
-- At most one working link per room.
CREATE UNIQUE INDEX idx_room_feed_tokens_active ON room_feed_tokens (room_id) WHERE revoked_at IS NULL;

-- +goose Down
DROP TABLE room_feed_tokens;
