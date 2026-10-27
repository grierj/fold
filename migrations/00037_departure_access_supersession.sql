-- +goose Up
-- A retained departure date must use departure provenance so its authority ends
-- at the exact occurrence boundary. When a manager-add row already supplies the
-- date, keep that immutable history and end it with this dedicated reason before
-- inserting the departure-origin successor.
ALTER TABLE occurrence_accesses DROP CONSTRAINT occurrence_accesses_end_state;
ALTER TABLE occurrence_accesses ADD CONSTRAINT occurrence_accesses_end_state CHECK (
    (status = 'active' AND ended_at IS NULL AND end_reason = '' AND ended_by IS NULL)
    OR (status = 'ended' AND ended_at IS NOT NULL AND ended_at >= granted_at AND (
        (end_reason IN ('declined', 'blocked') AND ended_by IS NOT NULL AND ended_by = subject_user_id)
        OR (end_reason IN ('removed', 'departure_superseded') AND ended_by IS NOT NULL)
        OR (end_reason IN ('occurrence_cancelled', 'occurrence_ended', 'occurrence_deleted', 'event_deleted', 'group_deleted',
            'account_disabled', 'consent_lost', 'context_changed') AND ended_by IS NULL))));

-- +goose Down
-- The older constraint cannot represent a manager grant that was superseded by
-- departure. Refuse to erase that accurate terminal history.
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM occurrence_accesses WHERE end_reason = 'departure_superseded') THEN
        RAISE EXCEPTION 'departure supersession history exists; cannot downgrade';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE occurrence_accesses DROP CONSTRAINT occurrence_accesses_end_state;
ALTER TABLE occurrence_accesses ADD CONSTRAINT occurrence_accesses_end_state CHECK (
    (status = 'active' AND ended_at IS NULL AND end_reason = '' AND ended_by IS NULL)
    OR (status = 'ended' AND ended_at IS NOT NULL AND ended_at >= granted_at AND (
        (end_reason IN ('declined', 'blocked') AND ended_by IS NOT NULL AND ended_by = subject_user_id)
        OR (end_reason = 'removed' AND ended_by IS NOT NULL)
        OR (end_reason IN ('occurrence_cancelled', 'occurrence_ended', 'occurrence_deleted', 'event_deleted', 'group_deleted',
            'account_disabled', 'consent_lost', 'context_changed') AND ended_by IS NULL))));
