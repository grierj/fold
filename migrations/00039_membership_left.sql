-- +goose Up
-- DEC-86: preserve a voluntary departure without imposing a removal ban.
ALTER TABLE memberships DROP CONSTRAINT memberships_status_check;
ALTER TABLE memberships ADD CONSTRAINT memberships_status_check
    CHECK (status IN ('active', 'removed', 'left'));

-- Repair the M1.14 self-leave bug only when the latest membership audit for
-- this person/group is an explicit, self-acted departure. A newer manager
-- removal/add/role change wins. Missing or ambiguous history stays removed;
-- never guess that a manager's ban was voluntary. Deleted accounts stay put.
-- Append a system audit in the same migration transaction; retain the original.
WITH latest AS (
    SELECT DISTINCT ON (m.id)
           m.id, m.user_id, m.group_id, a.id AS departure_audit_id,
           a.actor_id, a.action, a.meta_json
    FROM memberships AS m
    JOIN users AS u ON u.id = m.user_id AND u.deleted_at IS NULL
    JOIN audit_logs AS a ON a.target_type = 'group' AND a.target_id = m.group_id
        AND a.meta_json ->> 'userId' = m.user_id::text
        AND a.action LIKE 'membership.%'
    WHERE m.status = 'removed'
    ORDER BY m.id, a.id DESC
), corrected AS (
    UPDATE memberships AS m SET status = 'left'
    FROM latest AS a
    WHERE m.id = a.id AND m.status = 'removed'
        AND a.action = 'membership.role_changed' AND a.actor_id = a.user_id
        AND a.meta_json ->> 'newRole' = 'none'
        AND a.meta_json ->> 'oldRole' IN ('member', 'leader')
        AND a.meta_json ? 'keptCount' AND a.meta_json ? 'endedCount'
    RETURNING m.user_id, m.group_id, m.tenure, a.departure_audit_id
)
INSERT INTO audit_logs (actor_id, action, target_type, target_id, meta_json, at)
SELECT NULL, 'membership.departure_corrected', 'group', group_id,
       jsonb_build_object('userId', user_id, 'oldStatus', 'removed',
                          'newStatus', 'left', 'tenure', tenure,
                          'departureAuditId', departure_audit_id), now()
FROM corrected;

-- +goose Down
-- Correction audit rows remain append-only history after a successful rollback.
LOCK TABLE memberships IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM memberships WHERE status = 'left') THEN
        RAISE EXCEPTION 'left memberships exist; cannot downgrade safely';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE memberships DROP CONSTRAINT memberships_status_check;
ALTER TABLE memberships ADD CONSTRAINT memberships_status_check
    CHECK (status IN ('active', 'removed'));
