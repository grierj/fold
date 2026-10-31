-- +goose Up
-- Email suppressions (M3.2b, PLAN 5.3, DEC-112): an address Fold stops
-- mailing after its provider reports a hard bounce or a spam complaint, or
-- an admin suppresses it. A hard bounce or a manual suppression stops all
-- mail; a complaint stops optional and broadcast mail only, so account mail
-- (password resets, sign-in links) still goes. Addresses are stored
-- lowercased and trimmed. Clearing one (M3.2c) keeps the row as history.
CREATE TABLE suppressions (
    id bigserial PRIMARY KEY,
    channel text NOT NULL DEFAULT 'email' CHECK (channel IN ('email', 'sms')),
    address text NOT NULL CHECK (address = lower(btrim(address)) AND length(address) BETWEEN 3 AND 320),
    reason text NOT NULL CHECK (reason IN ('hard_bounce', 'complaint', 'manual')),
    provider text NOT NULL DEFAULT '',
    detail text NOT NULL DEFAULT '' CHECK (length(detail) <= 200),
    created_at timestamptz NOT NULL DEFAULT now(),
    cleared_at timestamptz,
    cleared_by bigint REFERENCES users(id) ON DELETE SET NULL
);
-- One open suppression per address and reason; sending looks them up by address.
CREATE UNIQUE INDEX idx_suppressions_open ON suppressions (channel, address, reason) WHERE cleared_at IS NULL;
-- Bounce reports find the delivery they are about by the provider's id.
CREATE INDEX idx_notification_deliveries_provider ON notification_deliveries (provider_message_id) WHERE provider_message_id IS NOT NULL;

-- +goose Down
DROP INDEX idx_notification_deliveries_provider;
DROP TABLE suppressions;
