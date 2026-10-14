-- +goose Up
-- M1.2 (C6, 5.6, 5.12, 10.1): email basics.
--
-- mail_outbox: the devoutbox sender (dev and tests) stores mail here;
-- /dev/outbox shows it and tests read links from it.
-- account_tokens: single-use hashed tokens for password resets and
-- email-change verifications. The raw token goes to the person's email;
-- only its SHA-256 is stored. Tokens expire after 24 hours (10.1).
-- invitations: admin and leader invitations (no approval step, DEC-4). A
-- leader's invitation names only a group they lead. over_limit records the
-- inviting leader's or admin's explicit choice to join over the group's
-- member limit (DEC-39).
-- notification_deliveries: one row per (notification, channel) email send
-- (5.3). M1.2 sends email right after the writing transaction commits;
-- the jobs table and the 15-minute escalation arrive in M3.1.

CREATE TABLE mail_outboxes (
    id bigserial PRIMARY KEY,
    to_address text NOT NULL,
    from_address text NOT NULL DEFAULT '',
    subject text NOT NULL,
    body text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_mail_outbox_created ON mail_outboxes (created_at DESC);

CREATE TABLE account_tokens (
    id bigserial PRIMARY KEY,
    kind text NOT NULL CHECK (kind IN ('password_reset', 'email_verify')),
    user_id bigint NOT NULL CONSTRAINT fk_account_tokens_user REFERENCES users (id) ON DELETE CASCADE,
    token_hash text NOT NULL,
    payload jsonb NOT NULL DEFAULT '{}',
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_account_tokens_token_hash ON account_tokens (token_hash);
CREATE INDEX idx_account_tokens_user_id ON account_tokens (user_id);

CREATE TABLE invitations (
    id bigserial PRIMARY KEY,
    email citext NOT NULL,
    phone text NOT NULL DEFAULT '',
    name text NOT NULL DEFAULT '',
    invited_by bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role text NOT NULL DEFAULT 'member' CHECK (role IN ('member', 'leader')),
    account_type text NOT NULL DEFAULT 'adult' CHECK (account_type IN ('adult', 'teen')),
    group_id bigint CONSTRAINT fk_invitations_group REFERENCES groups (id) ON DELETE CASCADE,
    over_limit boolean NOT NULL DEFAULT false,
    token_hash text NOT NULL,
    expires_at timestamptz NOT NULL,
    accepted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_invitations_token_hash ON invitations (token_hash);
CREATE INDEX idx_invitations_email ON invitations (email);
CREATE INDEX idx_invitations_group_id ON invitations (group_id);
CREATE INDEX idx_invitations_invited_by ON invitations (invited_by, created_at DESC);

CREATE TABLE notification_deliveries (
    id bigserial PRIMARY KEY,
    notification_id bigint NOT NULL REFERENCES notifications (id) ON DELETE CASCADE,
    user_id bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    channel text NOT NULL CHECK (channel IN ('email', 'sms')),
    status text NOT NULL CHECK (status IN ('queued', 'suppressed', 'deferred', 'sent', 'delivered', 'bounced', 'failed')),
    reason text NOT NULL DEFAULT '',
    scheduled_for timestamptz,
    provider_message_id text,
    sent_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_notification_deliveries_channel ON notification_deliveries (notification_id, channel);
CREATE INDEX idx_notification_deliveries_user_id ON notification_deliveries (user_id);

-- The email rendering of a notification: the link-only variant for
-- sensitive kinds (RenderEmailSafe), the in-app text otherwise (5.12).
ALTER TABLE notifications ADD COLUMN email_title text NOT NULL DEFAULT '';
ALTER TABLE notifications ADD COLUMN email_body text NOT NULL DEFAULT '';

-- +goose Down
DROP TABLE notification_deliveries;
DROP TABLE invitations;
DROP TABLE account_tokens;
DROP TABLE mail_outboxes;
