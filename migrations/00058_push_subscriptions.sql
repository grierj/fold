-- +goose Up
-- Web push (M3.13a, PLAN 5.3, DEC-125). One row per browser installation
-- that turned on notifications. The endpoint is a capability (whoever holds
-- it with the keys can push to the device), so it is never logged or
-- returned. Send results live in notification_deliveries, not here.
CREATE TABLE push_subscriptions (
    id bigserial PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    endpoint text NOT NULL UNIQUE CHECK (octet_length(endpoint) <= 2048),
    p256dh text NOT NULL CHECK (octet_length(p256dh) <= 200),
    auth text NOT NULL CHECK (octet_length(auth) <= 100),
    expires_at timestamptz,
    -- "iPhone · Home Screen app": derived on the server, never free text.
    label text NOT NULL DEFAULT '' CHECK (char_length(label) <= 80),
    created_at timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz NOT NULL DEFAULT now(),
    failed_at timestamptz
);
CREATE INDEX idx_push_subscriptions_user_id ON push_subscriptions (user_id);

-- Push joins email and SMS as a consent and delivery channel (DEC-122).
ALTER TABLE consents DROP CONSTRAINT consents_channel_check;
ALTER TABLE consents ADD CONSTRAINT consents_channel_check CHECK (channel IN ('email', 'sms', 'push'));
ALTER TABLE notification_deliveries DROP CONSTRAINT notification_deliveries_channel_check;
ALTER TABLE notification_deliveries ADD CONSTRAINT notification_deliveries_channel_check CHECK (channel IN ('email', 'sms', 'push'));

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM push_subscriptions)
        OR EXISTS (SELECT 1 FROM consents WHERE channel = 'push')
        OR EXISTS (SELECT 1 FROM notification_deliveries WHERE channel = 'push') THEN
        RAISE EXCEPTION 'push devices, consent or deliveries exist; restore a pre-upgrade backup to roll back';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE notification_deliveries DROP CONSTRAINT notification_deliveries_channel_check;
ALTER TABLE notification_deliveries ADD CONSTRAINT notification_deliveries_channel_check CHECK (channel IN ('email', 'sms'));
ALTER TABLE consents DROP CONSTRAINT consents_channel_check;
ALTER TABLE consents ADD CONSTRAINT consents_channel_check CHECK (channel IN ('email', 'sms'));
DROP TABLE push_subscriptions;
