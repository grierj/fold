-- +goose Up
-- Retained positive IDs intentionally have no account/context foreign keys:
-- safeguarding facts must survive hard deletion, like audit_logs. Live access
-- is always revalidated. AuditLog, not these outcome rows, owns attempt history.
CREATE TABLE contact_requests (
    id bigserial PRIMARY KEY,
    requester_id bigint NOT NULL CHECK (requester_id > 0),
    subject_user_id bigint NOT NULL CHECK (subject_user_id > 0 AND subject_user_id <> requester_id),
    context_type text NOT NULL CHECK (context_type IN ('group', 'event')),
    context_id bigint NOT NULL CHECK (context_id > 0),
    fields text NOT NULL CHECK (fields IN ('email', 'phone', 'both')),
    reason text NOT NULL DEFAULT '' CHECK (char_length(reason) <= 1000),
    needs_admin boolean NOT NULL,
    subject_decision text NOT NULL DEFAULT 'pending' CHECK (subject_decision IN ('pending', 'approved', 'denied')),
    subject_decided_by bigint,
    subject_decided_at timestamptz,
    admin_decision text NOT NULL CHECK (admin_decision IN ('pending', 'approved', 'denied', 'not_needed')),
    admin_decided_by bigint,
    admin_decided_at timestamptz,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'granted', 'denied', 'withdrawn', 'expired', 'ended')),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    completed_at timestamptz,
    withdrawn_by_id bigint,
    end_reason text NOT NULL DEFAULT '',
    CONSTRAINT contact_requests_scope UNIQUE (id, requester_id, subject_user_id, context_type, context_id, fields),
    CHECK (expires_at > created_at),
    CHECK ((subject_decision = 'pending' AND subject_decided_by IS NULL AND subject_decided_at IS NULL)
        OR (subject_decision <> 'pending' AND subject_decided_by IS NOT NULL AND subject_decided_by = subject_user_id AND subject_decided_at IS NOT NULL)),
    CHECK ((needs_admin AND admin_decision <> 'not_needed') OR (NOT needs_admin AND admin_decision = 'not_needed')),
    CHECK ((admin_decision IN ('pending', 'not_needed') AND admin_decided_by IS NULL AND admin_decided_at IS NULL)
        OR (admin_decision IN ('approved', 'denied') AND admin_decided_by IS NOT NULL AND admin_decided_by > 0 AND admin_decided_at IS NOT NULL)),
    CHECK (subject_decided_at IS NULL OR (subject_decided_at >= created_at AND subject_decided_at < expires_at)),
    CHECK (admin_decided_at IS NULL OR (admin_decided_at >= created_at AND admin_decided_at < expires_at)),
    CHECK ((status = 'pending' AND completed_at IS NULL) OR (status <> 'pending' AND completed_at IS NOT NULL AND completed_at >= created_at)),
    CHECK (completed_at IS NULL OR (subject_decided_at IS NULL OR subject_decided_at <= completed_at)
        AND (admin_decided_at IS NULL OR admin_decided_at <= completed_at)),
    CHECK ((status = 'withdrawn' AND withdrawn_by_id IS NOT NULL AND withdrawn_by_id = requester_id)
        OR (status <> 'withdrawn' AND withdrawn_by_id IS NULL)),
    CHECK ((status = 'ended' AND end_reason IN ('left_role', 'left_context', 'context_ended', 'turned_18', 'account_disabled', 'event_ended'))
        OR (status <> 'ended' AND end_reason = '')),
    CHECK ((status = 'granted' AND subject_decision = 'approved' AND admin_decision IN ('approved', 'not_needed'))
        OR (status = 'denied' AND (subject_decision = 'denied' OR admin_decision = 'denied'))
        OR (status IN ('pending', 'withdrawn', 'expired', 'ended') AND subject_decision <> 'denied' AND admin_decision <> 'denied'
            AND NOT (subject_decision = 'approved' AND admin_decision IN ('approved', 'not_needed')))),
    CHECK (status NOT IN ('granted', 'denied', 'withdrawn') OR completed_at < expires_at),
    CHECK (status <> 'expired' OR completed_at >= expires_at)
);
CREATE INDEX contact_requests_open_requester ON contact_requests (requester_id, expires_at) WHERE status = 'pending';
CREATE INDEX contact_requests_subject ON contact_requests (subject_user_id, created_at);

CREATE TABLE contact_grants (
    id bigserial PRIMARY KEY,
    request_id bigint NOT NULL UNIQUE,
    leader_id bigint NOT NULL,
    subject_user_id bigint NOT NULL,
    context_type text NOT NULL,
    context_id bigint NOT NULL,
    fields text NOT NULL,
    granted_at timestamptz NOT NULL,
    ended_at timestamptz,
    end_reason text NOT NULL DEFAULT '',
    ended_by bigint,
    FOREIGN KEY (request_id, leader_id, subject_user_id, context_type, context_id, fields)
        REFERENCES contact_requests (id, requester_id, subject_user_id, context_type, context_id, fields),
    CHECK ((ended_at IS NULL AND end_reason = '' AND ended_by IS NULL)
        OR (ended_at IS NOT NULL AND ended_at >= granted_at AND (
            (end_reason IN ('revoked_by_subject', 'revoked_by_admin', 'revoked_by_guardian') AND ended_by IS NOT NULL AND ended_by > 0)
            OR (end_reason IN ('left_role', 'left_context', 'context_ended', 'turned_18', 'account_disabled', 'event_ended') AND ended_by IS NULL)))),
    CHECK (end_reason <> 'revoked_by_subject' OR ended_by = subject_user_id)
);
CREATE INDEX contact_grants_active_leader ON contact_grants (leader_id) WHERE ended_at IS NULL;
CREATE INDEX contact_grants_subject ON contact_grants (subject_user_id, granted_at);

-- Successful provenance is checked without coupling a mutable status to an FK.
-- Lifecycle transitions and the per-requester open cap still require API locks.
-- +goose StatementBegin
CREATE FUNCTION contact_grant_successful_request() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM 1 FROM contact_requests WHERE id = NEW.request_id AND status = 'granted'
        AND completed_at <= NEW.granted_at FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'contact grant requires a successful request' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER contact_grant_successful_request BEFORE INSERT OR UPDATE OF request_id, leader_id, subject_user_id, context_type, context_id, fields, granted_at ON contact_grants
    FOR EACH ROW EXECUTE FUNCTION contact_grant_successful_request();

-- +goose Down
DROP TABLE contact_grants;
DROP FUNCTION contact_grant_successful_request();
DROP TABLE contact_requests;
