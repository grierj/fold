-- +goose Up
-- M7.4 R4-T07A, DEC-141's Nov 3, 2026 amendment (owner decision 4) and
-- inventory T07 and 15.3. Account erasure must blank the add_childcare_ages_note
-- of a deleted subject's occurrence_add requests, at every status, and the
-- baseline triggers refuse every change to that note. This migration lets two
-- functions allow exactly that one blank and nothing else. reason is not
-- touched: the owner's answer on it belongs to R4-T07B.
--
-- What each function now allows. add_request_identity() admits an UPDATE of an
-- occurrence_add row when the note goes from non-empty to '' and every other
-- column is equal (compared as a whole row, so a later column stays protected
-- when its type has an equality operator; a later column of a type without one,
-- such as json, point or xml, would make the comparison raise instead of
-- pass, so a migration that adds one to contact_requests must re-review both
-- functions), and the subject's users row exists, is soft-deleted and can be
-- share-locked without waiting (G-7c, FOR SHARE OF u NOWAIT). add_request_completed()
-- skips its completion recheck for the deferred event of exactly that UPDATE on
-- a granted occurrence_add row. Everything else, every INSERT, every move into
-- granted, every no-op UPDATE and every other UPDATE, runs the baseline body
-- unchanged, byte for byte, after the new statement.
--
-- Two functions, one exception: the second function is needed because the
-- recheck would otherwise fail at COMMIT on historical granted rows whose
-- registration was deleted or whose access_request_id moved to a newer request.
-- The completion exception does not look at the user itself. It relies on
-- add_request_identity() staying installed, unchanged in timing, as the only
-- BEFORE UPDATE trigger on contact_requests, so that every UPDATE reaching it
-- passed the identity check first.
--
-- Why the deleted subject is share-locked here while 00003 reads it unlocked
-- (T07A-P4): the licence is the soft delete, and a blank issued outside the
-- erasure must not race a later clearing of deleted_at, so the lookup holds the
-- user row. 00003's signed shape relies on setConsent's users-row lock, which
-- is a property of the traced application writers, not a guard against every SQL
-- client. Each is kept as signed.
--
-- The migration disables no trigger, creates or drops none, changes no table,
-- constraint, index or setting, and backfills nothing. The scoped reading of
-- hard constraint H8 (RC-1) governs this file's own text: it contains none of
-- the words that name a trigger bypass, a trigger DDL statement or a session
-- firing-mode change, and a test reads the file to prove it.
--
-- DEC-138 and PLAN 14.1: this is an additive expand step. It replaces two
-- function bodies and drops or renames nothing the previous release reads or
-- writes. The previous binary never sends a blank of the ages note, so both
-- bodies behave for it exactly as before. It is not a contract migration (no
-- contracts header), and no later migration needs to name it. Down restores the
-- definitions that came immediately before this file and never restores scrubbed
-- text.
--
-- LOCK: the migration can wait behind an ACCESS EXCLUSIVE lock held on
-- contact_requests. Both triggers are unchanged.
-- lock_timeout bounds each wait to acquire a lock, not the hold.
SET LOCAL lock_timeout = '5s';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.add_request_identity() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    v public.contact_requests%ROWTYPE;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF OLD.purpose = 'occurrence_add' AND OLD.add_childcare_ages_note <> ''
           AND NEW.add_childcare_ages_note = '' THEN
            v := NEW;
            v.add_childcare_ages_note := OLD.add_childcare_ages_note;
            IF v IS NOT DISTINCT FROM OLD THEN
                PERFORM 1 FROM public.users u
                    WHERE u.id = OLD.subject_user_id AND u.deleted_at IS NOT NULL
                    FOR SHARE OF u NOWAIT;
                IF FOUND THEN
                    RETURN NEW;
                END IF;
            END IF;
        END IF;
    END IF;
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

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.add_request_completed() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    v public.contact_requests%ROWTYPE;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF OLD.purpose = 'occurrence_add' AND OLD.status = 'granted'
           AND OLD.add_childcare_ages_note <> '' AND NEW.add_childcare_ages_note = '' THEN
            v := NEW;
            v.add_childcare_ages_note := OLD.add_childcare_ages_note;
            IF v IS NOT DISTINCT FROM OLD THEN
                RETURN NULL;
            END IF;
        END IF;
    END IF;
    IF NEW.purpose = 'group_add' AND NEW.status = 'granted' THEN
        PERFORM 1 FROM memberships m JOIN users u ON u.id = m.user_id JOIN groups g ON g.id = m.group_id
            WHERE m.id = NEW.completed_target_id AND m.user_id = NEW.subject_user_id
            AND m.group_id = NEW.context_id AND m.status = 'active' AND g.status = 'active' AND g.deleted_at IS NULL
            AND u.status = 'active' AND u.deleted_at IS NULL AND (NEW.needs_admin OR u.account_type <> 'teen');
        IF NOT FOUND THEN RAISE EXCEPTION 'group add requires completed membership' USING ERRCODE = '23514'; END IF;
    ELSIF NEW.purpose = 'occurrence_add' AND NEW.status = 'granted' THEN
        PERFORM 1 FROM registrations r JOIN event_occurrences o ON o.id = r.occurrence_id JOIN events e ON e.id = o.event_id
            WHERE r.id = NEW.completed_target_id AND r.access_request_id = NEW.id
            AND r.user_id = NEW.subject_user_id AND r.access_issuer_id = NEW.requester_id
            AND r.occurrence_id = NEW.occurrence_id AND e.id = NEW.context_id;
        IF NOT FOUND THEN RAISE EXCEPTION 'date add requires completed occurrence access' USING ERRCODE = '23514'; END IF;
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose Down
SET LOCAL lock_timeout = '5s';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.add_request_identity() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
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

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.add_request_completed() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.purpose = 'group_add' AND NEW.status = 'granted' THEN
        PERFORM 1 FROM memberships m JOIN users u ON u.id = m.user_id JOIN groups g ON g.id = m.group_id
            WHERE m.id = NEW.completed_target_id AND m.user_id = NEW.subject_user_id
            AND m.group_id = NEW.context_id AND m.status = 'active' AND g.status = 'active' AND g.deleted_at IS NULL
            AND u.status = 'active' AND u.deleted_at IS NULL AND (NEW.needs_admin OR u.account_type <> 'teen');
        IF NOT FOUND THEN RAISE EXCEPTION 'group add requires completed membership' USING ERRCODE = '23514'; END IF;
    ELSIF NEW.purpose = 'occurrence_add' AND NEW.status = 'granted' THEN
        PERFORM 1 FROM registrations r JOIN event_occurrences o ON o.id = r.occurrence_id JOIN events e ON e.id = o.event_id
            WHERE r.id = NEW.completed_target_id AND r.access_request_id = NEW.id
            AND r.user_id = NEW.subject_user_id AND r.access_issuer_id = NEW.requester_id
            AND r.occurrence_id = NEW.occurrence_id AND e.id = NEW.context_id;
        IF NOT FOUND THEN RAISE EXCEPTION 'date add requires completed occurrence access' USING ERRCODE = '23514'; END IF;
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
