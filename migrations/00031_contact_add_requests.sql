-- +goose Up
-- The retained IDs below deliberately have no live-domain foreign keys (00030).
-- API writes must take the contact advisory gate FIRST, then existing domain
-- locks; capacity/childcare checks and audit/notices share the final transaction.
ALTER TABLE contact_requests
    ADD COLUMN purpose text NOT NULL DEFAULT 'contact',
    ADD COLUMN occurrence_id bigint,
    ADD COLUMN add_over_limit boolean NOT NULL DEFAULT false,
    ADD COLUMN add_over_capacity boolean NOT NULL DEFAULT false,
    ADD COLUMN add_attendance_mode text NOT NULL DEFAULT '',
    ADD COLUMN add_guest_count bigint NOT NULL DEFAULT 0,
    ADD COLUMN add_childcare_count bigint NOT NULL DEFAULT 0,
    ADD COLUMN add_childcare_ages_note text NOT NULL DEFAULT '',
    ADD COLUMN completed_target_id bigint;
ALTER TABLE contact_requests DROP CONSTRAINT contact_requests_fields_check;
-- Locate only the old combined outcome check, rather than relying on the
-- generated ordinal name of a table CHECK from 00030.
-- +goose StatementBegin
DO $$ DECLARE c record; BEGIN
    FOR c IN SELECT conname FROM pg_constraint WHERE conrelid = 'contact_requests'::regclass AND contype = 'c'
        AND pg_get_constraintdef(oid) LIKE '%status%'
        AND pg_get_constraintdef(oid) LIKE '%subject_decision%'
        AND pg_get_constraintdef(oid) LIKE '%admin_decision%'
    LOOP EXECUTE format('ALTER TABLE contact_requests DROP CONSTRAINT %I', c.conname); END LOOP;
    FOR c IN SELECT conname FROM pg_constraint WHERE conrelid = 'contact_requests'::regclass AND contype = 'c'
        AND pg_get_constraintdef(oid) LIKE '%status%' AND pg_get_constraintdef(oid) LIKE '%end_reason%'
    LOOP EXECUTE format('ALTER TABLE contact_requests DROP CONSTRAINT %I', c.conname); END LOOP;
END $$;
-- +goose StatementEnd
ALTER TABLE contact_requests
    ADD CONSTRAINT contact_requests_end_reason CHECK (
        (status = 'ended' AND (end_reason IN ('left_role', 'left_context', 'context_ended', 'turned_18', 'account_disabled', 'event_ended')
            OR (purpose <> 'contact' AND end_reason IN ('blocked', 'removed', 'occurrence_cancelled', 'occurrence_deleted',
                'event_deleted', 'group_deleted', 'consent_lost', 'context_changed', 'already_added'))))
        OR (status <> 'ended' AND end_reason = '')),
    ADD CONSTRAINT contact_requests_purpose_scope CHECK (
        (purpose = 'contact' AND fields IN ('email', 'phone', 'both') AND occurrence_id IS NULL
            AND NOT add_over_limit AND NOT add_over_capacity AND add_attendance_mode = ''
            AND add_guest_count = 0 AND add_childcare_count = 0 AND add_childcare_ages_note = '' AND completed_target_id IS NULL)
        OR (purpose = 'group_add' AND context_type = 'group' AND fields = '' AND occurrence_id IS NULL
            AND NOT add_over_capacity AND add_attendance_mode = '' AND add_guest_count = 0
            AND add_childcare_count = 0 AND add_childcare_ages_note = '')
        OR (purpose = 'occurrence_add' AND context_type = 'event' AND fields = '' AND occurrence_id IS NOT NULL AND occurrence_id > 0
            AND NOT add_over_limit AND add_attendance_mode IN ('in_person', 'remote')
            AND add_guest_count BETWEEN 0 AND 5 AND add_childcare_count BETWEEN 0 AND 20
            AND char_length(add_childcare_ages_note) <= 200)),
    ADD CONSTRAINT contact_requests_add_completion CHECK (
        purpose = 'contact' OR (status = 'granted' AND completed_target_id IS NOT NULL AND completed_target_id > 0)
        OR (status <> 'granted' AND completed_target_id IS NULL)),
    ADD CONSTRAINT contact_requests_outcome CHECK (
        (status = 'granted' AND subject_decision = 'approved' AND admin_decision IN ('approved', 'not_needed'))
        OR (status = 'denied' AND (subject_decision = 'denied' OR admin_decision = 'denied'))
        OR (status IN ('pending', 'withdrawn', 'expired', 'ended') AND subject_decision <> 'denied' AND admin_decision <> 'denied'
            AND (purpose <> 'contact' OR NOT (subject_decision = 'approved' AND admin_decision IN ('approved', 'not_needed')))));

-- Contact grants can NEVER be produced by a successful add request.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION contact_grant_successful_request() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM 1 FROM contact_requests WHERE id = NEW.request_id AND purpose = 'contact' AND status = 'granted'
        AND completed_at <= NEW.granted_at FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'contact grant requires a successful contact request' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- One retained invitation/provenance record for EVERY manager account date
-- add, including ordinary visible dates. Not an RSVP or a contact grant.
-- Ended rows are immutable history. At most one active invitation per date;
-- only a NEW approved request may re-invite after an end, never a direct add.
CREATE TABLE occurrence_accesses (
    id bigserial PRIMARY KEY,
    subject_user_id bigint NOT NULL CHECK (subject_user_id > 0),
    occurrence_id bigint NOT NULL CHECK (occurrence_id > 0),
    event_id bigint NOT NULL CHECK (event_id > 0),
    organizer_type text NOT NULL CHECK (organizer_type IN ('group', 'event')),
    organizer_id bigint NOT NULL CHECK (organizer_id > 0),
    issuer_id bigint NOT NULL CHECK (issuer_id > 0 AND issuer_id <> subject_user_id),
    request_id bigint UNIQUE REFERENCES contact_requests(id),
    registration_id bigint NOT NULL CHECK (registration_id > 0),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'ended')),
    granted_at timestamptz NOT NULL DEFAULT now(),
    ended_at timestamptz,
    end_reason text NOT NULL DEFAULT '',
    ended_by bigint CHECK (ended_by > 0),
    CHECK (organizer_type <> 'event' OR organizer_id = event_id),
    CHECK ((status = 'active' AND ended_at IS NULL AND end_reason = '' AND ended_by IS NULL)
        OR (status = 'ended' AND ended_at IS NOT NULL AND ended_at >= granted_at AND (
            (end_reason IN ('declined', 'blocked') AND ended_by IS NOT NULL AND ended_by = subject_user_id)
            OR (end_reason = 'removed' AND ended_by IS NOT NULL)
            OR (end_reason IN ('occurrence_cancelled', 'occurrence_deleted', 'event_deleted', 'group_deleted',
                'account_disabled', 'consent_lost', 'context_changed') AND ended_by IS NULL))))
);
CREATE UNIQUE INDEX occurrence_accesses_one_active ON occurrence_accesses(subject_user_id, occurrence_id) WHERE status = 'active';
CREATE INDEX occurrence_accesses_event ON occurrence_accesses(event_id);

-- Organizer is a group for a group event, the event itself for church-wide.
-- These private retained IDs must never be emitted as hidden-group UI data.
CREATE TABLE manager_add_blocks (
    id bigserial PRIMARY KEY,
    subject_user_id bigint NOT NULL CHECK (subject_user_id > 0),
    organizer_type text NOT NULL CHECK (organizer_type IN ('group', 'event')),
    organizer_id bigint NOT NULL CHECK (organizer_id > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (subject_user_id, organizer_type, organizer_id)
);

-- Validate live provenance at creation, not with cascading foreign keys. Reads
-- must ALSO reload live subject, occurrence, event and organizer facts. Pending
-- asks depend on issuer management; accepted invitations intentionally do not.
-- +goose StatementBegin
CREATE FUNCTION occurrence_access_provenance() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'occurrence access history cannot be deleted' USING ERRCODE = '23514';
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF ROW(NEW.subject_user_id, NEW.occurrence_id, NEW.event_id, NEW.organizer_type, NEW.organizer_id,
            NEW.issuer_id, NEW.request_id, NEW.registration_id, NEW.granted_at)
            IS DISTINCT FROM ROW(OLD.subject_user_id, OLD.occurrence_id, OLD.event_id, OLD.organizer_type, OLD.organizer_id,
            OLD.issuer_id, OLD.request_id, OLD.registration_id, OLD.granted_at)
            OR (OLD.status = 'ended' AND NEW IS DISTINCT FROM OLD) THEN
            RAISE EXCEPTION 'occurrence access provenance and terminal state are immutable' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    PERFORM 1 FROM registrations r JOIN event_occurrences o ON o.id = r.occurrence_id
        JOIN events e ON e.id = o.event_id JOIN users u ON u.id = r.user_id
        LEFT JOIN groups g ON g.id = e.group_id
        WHERE r.id = NEW.registration_id AND r.user_id = NEW.subject_user_id AND r.status = 'going'
        AND o.id = NEW.occurrence_id AND o.status = 'scheduled' AND e.id = NEW.event_id AND e.deleted_at IS NULL
        AND u.status = 'active' AND u.deleted_at IS NULL
        AND ((e.group_id IS NULL AND NEW.organizer_type = 'event' AND NEW.organizer_id = e.id)
            OR (g.id = NEW.organizer_id AND NEW.organizer_type = 'group' AND g.status = 'active' AND g.deleted_at IS NULL));
    IF NOT FOUND THEN
        RAISE EXCEPTION 'occurrence access requires completed live account attendance' USING ERRCODE = '23514';
    END IF;
    IF NEW.status <> 'active' OR EXISTS (SELECT 1 FROM manager_add_blocks WHERE subject_user_id = NEW.subject_user_id
        AND organizer_type = NEW.organizer_type AND organizer_id = NEW.organizer_id) THEN
        RAISE EXCEPTION 'occurrence access blocked or not active' USING ERRCODE = '23514';
    END IF;
    IF NEW.request_id IS NULL AND (EXISTS (SELECT 1 FROM users WHERE id = NEW.subject_user_id AND account_type = 'teen')
        OR EXISTS (SELECT 1 FROM occurrence_accesses WHERE subject_user_id = NEW.subject_user_id AND occurrence_id = NEW.occurrence_id)) THEN
        RAISE EXCEPTION 'teen or repeat manager add requires a new approved request' USING ERRCODE = '23514';
    END IF;
    IF NEW.request_id IS NOT NULL THEN
        PERFORM 1 FROM contact_requests WHERE id = NEW.request_id AND purpose = 'occurrence_add' AND status = 'granted'
            AND requester_id = NEW.issuer_id AND subject_user_id = NEW.subject_user_id AND context_type = 'event'
            AND context_id = NEW.event_id AND occurrence_id = NEW.occurrence_id AND completed_target_id = NEW.registration_id
            AND completed_at <= NEW.granted_at
            AND NOT EXISTS (SELECT 1 FROM occurrence_accesses old_access WHERE old_access.subject_user_id = NEW.subject_user_id
                AND old_access.occurrence_id = NEW.occurrence_id AND old_access.status = 'ended'
                AND old_access.ended_at > contact_requests.created_at)
            AND (needs_admin OR NOT EXISTS (SELECT 1 FROM users WHERE id = NEW.subject_user_id AND account_type = 'teen')) FOR SHARE;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'occurrence access requires exact successful add request' USING ERRCODE = '23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER occurrence_access_provenance BEFORE INSERT OR UPDATE OR DELETE ON occurrence_accesses
    FOR EACH ROW EXECUTE FUNCTION occurrence_access_provenance();

-- Request options and successful outcomes cannot be retargeted/replayed. A
-- pending fully-approved add may still expire/end if capacity never permits it.
-- +goose StatementBegin
CREATE FUNCTION add_request_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.purpose <> OLD.purpose OR (OLD.purpose <> 'contact' AND (
        ROW(NEW.requester_id, NEW.subject_user_id, NEW.context_type, NEW.context_id, NEW.occurrence_id, NEW.needs_admin,
            NEW.add_over_limit, NEW.add_over_capacity, NEW.add_attendance_mode, NEW.add_guest_count,
            NEW.add_childcare_count, NEW.add_childcare_ages_note, NEW.created_at, NEW.expires_at)
        IS DISTINCT FROM ROW(OLD.requester_id, OLD.subject_user_id, OLD.context_type, OLD.context_id, OLD.occurrence_id, OLD.needs_admin,
            OLD.add_over_limit, OLD.add_over_capacity, OLD.add_attendance_mode, OLD.add_guest_count,
            OLD.add_childcare_count, OLD.add_childcare_ages_note, OLD.created_at, OLD.expires_at)
        OR (OLD.status <> 'pending' AND NEW IS DISTINCT FROM OLD))) THEN
        RAISE EXCEPTION 'request purpose, add options and terminal outcome are immutable' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER add_request_identity BEFORE UPDATE ON contact_requests FOR EACH ROW EXECUTE FUNCTION add_request_identity();

-- A granted add has an actual effect, not merely two approvals. Deferred so
-- the completion transaction can update the request then insert provenance.
-- +goose StatementBegin
CREATE FUNCTION add_request_completed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.purpose = 'group_add' AND NEW.status = 'granted' THEN
        PERFORM 1 FROM memberships m JOIN users u ON u.id = m.user_id JOIN groups g ON g.id = m.group_id
            WHERE m.id = NEW.completed_target_id AND m.user_id = NEW.subject_user_id
            AND m.group_id = NEW.context_id AND m.status = 'active' AND g.status = 'active' AND g.deleted_at IS NULL
            AND u.status = 'active' AND u.deleted_at IS NULL AND (NEW.needs_admin OR u.account_type <> 'teen');
        IF NOT FOUND THEN RAISE EXCEPTION 'group add requires completed membership' USING ERRCODE = '23514'; END IF;
    ELSIF NEW.purpose = 'occurrence_add' AND NEW.status = 'granted' THEN
        PERFORM 1 FROM occurrence_accesses WHERE request_id = NEW.id AND registration_id = NEW.completed_target_id
            AND subject_user_id = NEW.subject_user_id AND issuer_id = NEW.requester_id
            AND event_id = NEW.context_id AND occurrence_id = NEW.occurrence_id;
        IF NOT FOUND THEN RAISE EXCEPTION 'date add requires completed occurrence access' USING ERRCODE = '23514'; END IF;
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER add_request_completed AFTER INSERT OR UPDATE ON contact_requests
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION add_request_completed();

-- +goose Down
-- Never silently discard safeguarding history on downgrade.
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM contact_requests WHERE purpose <> 'contact')
        OR EXISTS (SELECT 1 FROM occurrence_accesses) OR EXISTS (SELECT 1 FROM manager_add_blocks) THEN
        RAISE EXCEPTION 'add safeguarding history exists; cannot downgrade';
    END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER add_request_completed ON contact_requests;
DROP FUNCTION add_request_completed();
DROP TRIGGER add_request_identity ON contact_requests;
DROP FUNCTION add_request_identity();
DROP TABLE manager_add_blocks;
DROP TABLE occurrence_accesses;
DROP FUNCTION occurrence_access_provenance();
ALTER TABLE contact_requests DROP CONSTRAINT contact_requests_purpose_scope,
    DROP CONSTRAINT contact_requests_add_completion, DROP CONSTRAINT contact_requests_outcome, DROP CONSTRAINT contact_requests_end_reason;
ALTER TABLE contact_requests
    DROP COLUMN purpose, DROP COLUMN occurrence_id, DROP COLUMN add_over_limit, DROP COLUMN add_over_capacity,
    DROP COLUMN add_attendance_mode, DROP COLUMN add_guest_count, DROP COLUMN add_childcare_count,
    DROP COLUMN add_childcare_ages_note, DROP COLUMN completed_target_id,
    ADD CHECK (fields IN ('email', 'phone', 'both')),
    ADD CHECK ((status = 'ended' AND end_reason IN ('left_role', 'left_context', 'context_ended', 'turned_18', 'account_disabled', 'event_ended'))
        OR (status <> 'ended' AND end_reason = '')),
    ADD CHECK ((status = 'granted' AND subject_decision = 'approved' AND admin_decision IN ('approved', 'not_needed'))
        OR (status = 'denied' AND (subject_decision = 'denied' OR admin_decision = 'denied'))
        OR (status IN ('pending', 'withdrawn', 'expired', 'ended') AND subject_decision <> 'denied' AND admin_decision <> 'denied'
            AND NOT (subject_decision = 'approved' AND admin_decision IN ('approved', 'not_needed'))));
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION contact_grant_successful_request() RETURNS trigger LANGUAGE plpgsql AS $$
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
