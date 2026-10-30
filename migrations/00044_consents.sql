-- +goose Up
-- Consent records (5.3, 5.12): append-only, and the latest row per
-- (user, channel, scope) is in force. DEC-97 writes the first ones: "Turn
-- off all optional email" stores an email opt-out per optional notification
-- category (scope 'category:<name>'). SMS consent and per-category switches
-- arrive with M3.12 on the same table. Consent history is evidence (TCPA,
-- CAN-SPAM; 5.3): it is never updated or deleted, not even with its user.
CREATE TABLE consents (
    id bigserial PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users(id),
    channel text NOT NULL CHECK (channel IN ('email', 'sms')),
    scope text NOT NULL CHECK (scope ~ '^(all|group:[0-9]+|category:(admin|groups|events|messages))$'),
    status text NOT NULL CHECK (status IN ('opted_in', 'opted_out')),
    source text NOT NULL CHECK (source IN ('web_form', 'sms_keyword', 'sms_reply', 'admin_import', 'unsubscribe_link', 'admin')),
    evidence_text text NOT NULL DEFAULT '' CHECK (octet_length(evidence_text) <= 1000),
    ip text NOT NULL DEFAULT '',
    user_agent text NOT NULL DEFAULT '',
    at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX idx_consents_latest ON consents(user_id, channel, scope, at DESC, id DESC);
-- +goose StatementBegin
CREATE FUNCTION reject_consent_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'consent history is append-only';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER consents_append_only BEFORE UPDATE OR DELETE ON consents
    FOR EACH ROW EXECUTE FUNCTION reject_consent_change();

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM consents) THEN
        RAISE EXCEPTION 'consent history exists; restore a pre-upgrade backup to roll back';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE consents;
DROP FUNCTION reject_consent_change();
