-- +goose Up
-- The send pipeline (M3.5, PLAN 7.1): message.send snapshots who a
-- scheduled message reaches into message_recipients, one row per person
-- and channel, and message.deliver_batch sends the queued rows. Skipped
-- people get a row too, with the reason, so history can count them. A
-- guardian copy (DEC-19) is a row for the guardian with copy_of_user_id
-- set to the teen. address stays in the worker: no API returns it (DEC-31).
CREATE TABLE message_recipients (
    id bigserial PRIMARY KEY,
    message_id bigint NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    user_id bigint REFERENCES users(id) ON DELETE SET NULL,
    channel text NOT NULL DEFAULT 'email' CHECK (channel IN ('email', 'sms')),
    address text NOT NULL DEFAULT '' CHECK (length(address) <= 320),
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'suppressed', 'deferred', 'sent', 'delivered', 'bounced', 'complained', 'failed')),
    reason text NOT NULL DEFAULT '' CHECK (length(reason) <= 200),
    copy_of_user_id bigint REFERENCES users(id) ON DELETE SET NULL,
    provider_message_id text NOT NULL DEFAULT '' CHECK (length(provider_message_id) <= 200),
    error text NOT NULL DEFAULT '' CHECK (length(error) <= 2000),
    sent_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT message_recipients_once UNIQUE NULLS NOT DISTINCT (message_id, user_id, channel, copy_of_user_id)
);
CREATE INDEX idx_message_recipients_message ON message_recipients (message_id, status);
-- Delivery and bounce reports find their row by the provider's id.
CREATE INDEX idx_message_recipients_provider ON message_recipients (provider_message_id) WHERE provider_message_id <> '';

-- Why a message failed or was refused at send time, for its author.
ALTER TABLE messages ADD COLUMN error text NOT NULL DEFAULT '' CHECK (length(error) <= 2000);
-- The dev outbox shows an announcement's Reply-To (DEC-130).
ALTER TABLE mail_outboxes ADD COLUMN reply_to text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE mail_outboxes DROP COLUMN reply_to;
ALTER TABLE messages DROP COLUMN error;
DROP TABLE message_recipients;
