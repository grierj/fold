-- +goose Up
-- DEC-74 retained dates extend occurrence_accesses; they are not a second
-- visibility mechanism. Existing rows are manager adds. Departure rows carry
-- the membership and actor that authorized retention instead of inventing an
-- issuer or add request.
ALTER TABLE occurrence_accesses
    ADD COLUMN origin text NOT NULL DEFAULT 'manager_add',
    ADD COLUMN departure_membership_id bigint CHECK (departure_membership_id > 0),
    ADD COLUMN departure_actor_id bigint CHECK (departure_actor_id > 0),
    ALTER COLUMN issuer_id DROP NOT NULL,
    ADD CONSTRAINT occurrence_accesses_origin_provenance CHECK (
        (origin = 'manager_add' AND issuer_id IS NOT NULL AND issuer_id > 0 AND issuer_id <> subject_user_id
            AND departure_membership_id IS NULL AND departure_actor_id IS NULL)
        OR (origin = 'departure_self' AND issuer_id IS NULL AND request_id IS NULL
            AND organizer_type = 'group' AND departure_membership_id IS NOT NULL
            AND departure_actor_id = subject_user_id)
        OR (origin = 'departure_removal' AND issuer_id IS NULL AND request_id IS NULL
            AND organizer_type = 'group' AND departure_membership_id IS NOT NULL
            AND departure_actor_id IS NOT NULL AND departure_actor_id <> subject_user_id));

-- Validate immutable origin-specific provenance at creation. Manager adds keep
-- every M1.11 safeguard. Departure retention accepts Going, Maybe or Waitlisted,
-- ignores manager-add blocks, and must be inserted while the locked membership
-- is still active so this trigger can validate self-leave/removal authority.
-- Losing that membership after commit intentionally does not end access.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION occurrence_access_provenance() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'occurrence access history cannot be deleted' USING ERRCODE = '23514';
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF ROW(NEW.subject_user_id, NEW.occurrence_id, NEW.event_id, NEW.organizer_type, NEW.organizer_id,
            NEW.origin, NEW.issuer_id, NEW.request_id, NEW.registration_id, NEW.departure_membership_id,
            NEW.departure_actor_id, NEW.granted_at)
            IS DISTINCT FROM ROW(OLD.subject_user_id, OLD.occurrence_id, OLD.event_id, OLD.organizer_type, OLD.organizer_id,
            OLD.origin, OLD.issuer_id, OLD.request_id, OLD.registration_id, OLD.departure_membership_id,
            OLD.departure_actor_id, OLD.granted_at)
            OR (OLD.status = 'ended' AND NEW IS DISTINCT FROM OLD) THEN
            RAISE EXCEPTION 'occurrence access provenance and terminal state are immutable' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    PERFORM 1 FROM registrations r JOIN event_occurrences o ON o.id = r.occurrence_id
        JOIN events e ON e.id = o.event_id JOIN users u ON u.id = r.user_id
        LEFT JOIN groups g ON g.id = e.group_id
        WHERE r.id = NEW.registration_id AND r.user_id = NEW.subject_user_id
        AND ((NEW.origin = 'manager_add' AND r.status = 'going')
            OR (NEW.origin IN ('departure_self', 'departure_removal') AND r.status IN ('going', 'maybe', 'waitlisted')))
        AND o.id = NEW.occurrence_id AND o.status = 'scheduled' AND e.id = NEW.event_id AND e.deleted_at IS NULL
        AND u.status = 'active' AND u.deleted_at IS NULL
        AND ((e.group_id IS NULL AND NEW.organizer_type = 'event' AND NEW.organizer_id = e.id)
            OR (g.id = NEW.organizer_id AND NEW.organizer_type = 'group' AND g.status = 'active' AND g.deleted_at IS NULL));
    IF NOT FOUND THEN
        RAISE EXCEPTION 'occurrence access requires completed live account attendance' USING ERRCODE = '23514';
    END IF;
    IF NEW.status <> 'active' THEN
        RAISE EXCEPTION 'occurrence access must start active' USING ERRCODE = '23514';
    END IF;

    IF NEW.origin = 'manager_add' THEN
        IF EXISTS (SELECT 1 FROM manager_add_blocks WHERE subject_user_id = NEW.subject_user_id
            AND organizer_type = NEW.organizer_type AND organizer_id = NEW.organizer_id) THEN
            RAISE EXCEPTION 'occurrence access blocked' USING ERRCODE = '23514';
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
    ELSIF NEW.origin = 'departure_self' THEN
        PERFORM 1 FROM memberships m
            WHERE m.id = NEW.departure_membership_id AND m.user_id = NEW.subject_user_id
            AND m.group_id = NEW.organizer_id AND m.status = 'active'
            AND (m.role <> 'leader' OR (SELECT count(*) FROM memberships leaders
                WHERE leaders.group_id = m.group_id AND leaders.status = 'active' AND leaders.role = 'leader') > 1)
            FOR SHARE;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'departure access requires authorized self-leave' USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.origin = 'departure_removal' THEN
        PERFORM 1 FROM memberships target
            JOIN users subject ON subject.id = target.user_id
            JOIN users actor ON actor.id = NEW.departure_actor_id
            WHERE target.id = NEW.departure_membership_id AND target.user_id = NEW.subject_user_id
            AND target.group_id = NEW.organizer_id AND target.status = 'active'
            AND actor.status = 'active' AND actor.deleted_at IS NULL
            AND (actor.role = 'admin' OR EXISTS (SELECT 1 FROM memberships manager
                WHERE manager.user_id = actor.id AND manager.group_id = target.group_id
                AND manager.status = 'active' AND manager.role = 'leader'))
            AND (subject.role <> 'admin' OR actor.role = 'admin')
            AND (target.role <> 'leader' OR (SELECT count(*) FROM memberships leaders
                WHERE leaders.group_id = target.group_id AND leaders.status = 'active' AND leaders.role = 'leader') > 1)
            FOR SHARE;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'departure access requires authorized member removal' USING ERRCODE = '23514';
        END IF;
    ELSE
        RAISE EXCEPTION 'unknown occurrence access origin' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- Manager-add rows predate this migration and remain representable. Refuse a
-- downgrade once departure provenance exists rather than silently relabeling it.
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM occurrence_accesses WHERE origin <> 'manager_add') THEN
        RAISE EXCEPTION 'departure occurrence access history exists; cannot downgrade';
    END IF;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION occurrence_access_provenance() RETURNS trigger LANGUAGE plpgsql AS $$
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

ALTER TABLE occurrence_accesses
    DROP CONSTRAINT occurrence_accesses_origin_provenance,
    DROP COLUMN origin,
    DROP COLUMN departure_membership_id,
    DROP COLUMN departure_actor_id,
    ALTER COLUMN issuer_id SET NOT NULL;
