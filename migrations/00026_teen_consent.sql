-- +goose Up
ALTER TABLE users ADD COLUMN administratively_disabled boolean NOT NULL DEFAULT false;
UPDATE users SET administratively_disabled = true WHERE status = 'disabled';

CREATE TABLE teen_consents (
    id bigserial PRIMARY KEY,
    teen_user_id bigint NOT NULL REFERENCES users(id),
    scope text NOT NULL CHECK (scope IN ('account', 'sms', 'photos')),
    status text NOT NULL CHECK (status IN ('granted', 'revoked')),
    granted_by bigint NOT NULL REFERENCES users(id),
    granter_role text NOT NULL CHECK (granter_role IN ('guardian', 'admin')),
    evidence_text text NOT NULL DEFAULT '' CHECK (octet_length(evidence_text) <= 1000),
    at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT teen_consents_admin_evidence CHECK (
        granter_role <> 'admin' OR status <> 'granted' OR length(btrim(evidence_text)) > 0
    )
);
CREATE INDEX idx_teen_consents_latest ON teen_consents(teen_user_id, scope, at DESC, id DESC);

-- +goose StatementBegin
CREATE FUNCTION reject_teen_consent_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'teen consent history is append-only';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER teen_consents_append_only BEFORE UPDATE OR DELETE ON teen_consents
    FOR EACH ROW EXECUTE FUNCTION reject_teen_consent_change();

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM teen_consents) THEN
        RAISE EXCEPTION 'teen consent history exists; restore a pre-upgrade backup to roll back';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE teen_consents;
DROP FUNCTION reject_teen_consent_change();
ALTER TABLE users DROP COLUMN administratively_disabled;
