-- +goose Up
-- DEC-74 retained departure access expires at the exact occurrence end. Keep a
-- distinct terminal reason so the irreversible history says why it ended;
-- manager-add invitations retain their established lifecycle.
-- +goose StatementBegin
DO $$ DECLARE c record; BEGIN
    FOR c IN SELECT conname FROM pg_constraint
        WHERE conrelid = 'occurrence_accesses'::regclass AND contype = 'c'
        AND pg_get_constraintdef(oid) LIKE '%end_reason%'
    LOOP EXECUTE format('ALTER TABLE occurrence_accesses DROP CONSTRAINT %I', c.conname); END LOOP;
END $$;
-- +goose StatementEnd
ALTER TABLE occurrence_accesses ADD CONSTRAINT occurrence_accesses_end_state CHECK (
    (status = 'active' AND ended_at IS NULL AND end_reason = '' AND ended_by IS NULL)
    OR (status = 'ended' AND ended_at IS NOT NULL AND ended_at >= granted_at AND (
        (end_reason IN ('declined', 'blocked') AND ended_by IS NOT NULL AND ended_by = subject_user_id)
        OR (end_reason = 'removed' AND ended_by IS NOT NULL)
        OR (end_reason IN ('occurrence_cancelled', 'occurrence_ended', 'occurrence_deleted', 'event_deleted', 'group_deleted',
            'account_disabled', 'consent_lost', 'context_changed') AND ended_by IS NULL))));

-- +goose Down
ALTER TABLE occurrence_accesses DROP CONSTRAINT occurrence_accesses_end_state;
ALTER TABLE occurrence_accesses ADD CONSTRAINT occurrence_accesses_end_state CHECK (
    (status = 'active' AND ended_at IS NULL AND end_reason = '' AND ended_by IS NULL)
    OR (status = 'ended' AND ended_at IS NOT NULL AND ended_at >= granted_at AND (
        (end_reason IN ('declined', 'blocked') AND ended_by IS NOT NULL AND ended_by = subject_user_id)
        OR (end_reason = 'removed' AND ended_by IS NOT NULL)
        OR (end_reason IN ('occurrence_cancelled', 'occurrence_deleted', 'event_deleted', 'group_deleted',
            'account_disabled', 'consent_lost', 'context_changed') AND ended_by IS NULL))));
