-- +goose Up
-- Messages (M3.4, PLAN 5.3, 7.1): an announcement a leader, host or admin
-- writes in the composer. M3.4 saves, schedules and cancels them; M3.5's
-- message.send job sends a scheduled one and records its recipients.
-- audience is one of the PLAN 7.2 shapes. body_html is already sanitized
-- (bluemonday) when it is stored. Columns for approvals (M3.9) and the SMS
-- send window (M3.7) arrive with the milestones that use them.
CREATE TABLE messages (
    id bigserial PRIMARY KEY,
    channel text NOT NULL DEFAULT 'email' CHECK (channel IN ('email', 'sms', 'both')),
    author_id bigint REFERENCES users(id) ON DELETE SET NULL,
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'scheduled', 'sending', 'sent', 'cancelled', 'failed')),
    category text NOT NULL DEFAULT 'announcement' CHECK (category IN ('announcement', 'reminder')),
    subject text NOT NULL DEFAULT '' CHECK (length(subject) <= 200),
    body_html text NOT NULL DEFAULT '' CHECK (length(body_html) <= 100000),
    body_text text NOT NULL DEFAULT '' CHECK (length(body_text) <= 50000),
    sms_body text NOT NULL DEFAULT '' CHECK (length(sms_body) <= 1600),
    audience jsonb NOT NULL DEFAULT '{}',
    scheduled_at timestamptz,
    sent_at timestamptz,
    segments_estimate bigint NOT NULL DEFAULT 0,
    recipient_count bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT messages_scheduled_has_time CHECK (status <> 'scheduled' OR scheduled_at IS NOT NULL)
);
CREATE INDEX idx_messages_author ON messages (author_id, updated_at DESC);
-- What M3.5's sender looks for.
CREATE INDEX idx_messages_due ON messages (scheduled_at) WHERE status = 'scheduled';

-- +goose Down
DROP TABLE messages;
