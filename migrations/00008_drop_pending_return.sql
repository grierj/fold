-- +goose Up
-- DEC-47 reverses the pending_return part of DEC-38/DEC-39. A re-enabled
-- account whose group is full is now left out and told to ask a leader.
-- Rows still waiting get the same outcome: the membership is dropped and a
-- membership.not_restored audit row (system actor) is written, so the
-- person sees the "ask a leader" notice. The status value then goes away
-- for good: memberships.status is only active or removed.

INSERT INTO audit_logs (actor_id, action, target_type, target_id, meta_json, at)
SELECT NULL, 'membership.not_restored', 'group', m.group_id,
       jsonb_build_object('userId', m.user_id, 'reason', 'pending_return_dropped'), now()
FROM memberships AS m
WHERE m.status = 'pending_return';

DELETE FROM memberships WHERE status = 'pending_return';

ALTER TABLE memberships ADD CONSTRAINT memberships_status_check
    CHECK (status IN ('active', 'removed'));

-- +goose Down
-- Dropped memberships are not brought back (the audit rows record who was
-- left out); only the constraint is removed so pending_return is allowed.
ALTER TABLE memberships DROP CONSTRAINT memberships_status_check;
