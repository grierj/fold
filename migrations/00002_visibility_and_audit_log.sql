-- +goose Up
-- M0.2: one visibility enum on groups (C3) and the audit log (5.3).

ALTER TABLE groups DROP COLUMN invite_only;
ALTER TABLE groups ADD CONSTRAINT groups_visibility_check
    CHECK (visibility IN ('public', 'private', 'invite_only'));

ALTER TABLE memberships ADD CONSTRAINT memberships_role_check
    CHECK (role IN ('leader', 'member'));

CREATE TABLE audit_logs (
    id bigserial PRIMARY KEY,
    actor_id bigint,
    action text NOT NULL,
    target_type text NOT NULL,
    target_id bigint NOT NULL,
    meta_json jsonb NOT NULL DEFAULT '{}',
    ip text,
    at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_audit_logs_actor_id ON audit_logs (actor_id);
CREATE INDEX idx_audit_logs_action ON audit_logs (action);
CREATE INDEX idx_audit_logs_target ON audit_logs (target_type, target_id);
CREATE INDEX idx_audit_logs_at ON audit_logs (at);

-- +goose Down
DROP TABLE audit_logs;
ALTER TABLE memberships DROP CONSTRAINT memberships_role_check;
ALTER TABLE groups DROP CONSTRAINT groups_visibility_check;
ALTER TABLE groups ADD COLUMN invite_only boolean NOT NULL DEFAULT false;
