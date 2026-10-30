-- +goose Up
-- Consent records (5.3, 5.12): append-only, and the latest row per
-- (user, channel, scope) is in force. DEC-97 writes the first ones: "Turn
-- off all optional email" stores an email opt-out per optional notification
-- category (scope 'category:<name>'). SMS consent and per-category switches
-- arrive with M3.12 on the same table.
CREATE TABLE consents (
    id bigserial PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    channel text NOT NULL CHECK (channel IN ('email', 'sms')),
    scope text NOT NULL CHECK (scope = 'all' OR scope LIKE 'group:%' OR scope LIKE 'category:%'),
    status text NOT NULL CHECK (status IN ('opted_in', 'opted_out')),
    source text NOT NULL CHECK (source IN ('web_form', 'sms_keyword', 'sms_reply', 'admin_import', 'unsubscribe_link', 'admin')),
    evidence_text text NOT NULL DEFAULT '' CHECK (octet_length(evidence_text) <= 1000),
    ip text NOT NULL DEFAULT '',
    user_agent text NOT NULL DEFAULT '',
    at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX idx_consents_latest ON consents(user_id, channel, scope, at DESC, id DESC);

-- +goose Down
DROP TABLE consents;
