-- +goose Up
-- M7.4 T07B-2a, DEC-141, owner decisions 6 and 7, approved T07B revision 8.
-- Expand only the two 00004 function bodies. A reason can go from nonempty to
-- empty on a non-contact, non-pending request with no other column changed when either party is soft-deleted and its
-- users row can be share-locked without waiting. The whole-row comparison also
-- protects future columns whose types have equality operators; other types fail
-- closed and need review when added.
--
-- The completion branch admits that same reason-only UPDATE on a granted add
-- request whose historical completion may no longer exist. It relies on the
-- unchanged BEFORE identity check for the deleted-party licence. All previous
-- statements, including the ages-note branches, stay byte-identical. Existing
-- contact and pending reason updates keep their baseline behavior.
--
-- There is no caller in this slice. Per-item admin authorization, decision audit
-- and review validation belong to the later reason-review behavior slices. The
-- functions enforce only the adopted database transition, not admin authority.
-- No draft storage, new index, backfill or text change is introduced here.
--
-- DEC-138 additive expansion: no previous-release object is dropped or renamed.
-- Down restores 00004's definitions verbatim and never restores blanked text.
-- The existing trigger installation, timing, deferral and firing mode stay as is.
-- lock_timeout bounds each wait for a conflicting lock, not its hold.
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
    IF TG_OP = 'UPDATE' AND OLD.purpose <> 'contact' AND OLD.status <> 'pending' AND OLD.reason <> '' AND NEW.reason = '' THEN
        v := NEW;
        v.reason := OLD.reason;
        IF v IS NOT DISTINCT FROM OLD THEN
            PERFORM 1 FROM public.users u
                WHERE u.id IN (OLD.requester_id, OLD.subject_user_id) AND u.deleted_at IS NOT NULL
                FOR SHARE OF u NOWAIT;
            IF FOUND THEN
                RETURN NEW;
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
    IF TG_OP = 'UPDATE' AND OLD.purpose IN ('group_add', 'occurrence_add')
       AND OLD.status = 'granted' AND OLD.reason <> '' AND NEW.reason = '' THEN
        v := NEW;
        v.reason := OLD.reason;
        IF v IS NOT DISTINCT FROM OLD THEN
            RETURN NULL;
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
