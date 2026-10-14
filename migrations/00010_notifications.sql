-- +goose Up
-- M1.1c (5.12, DEC-42): stored in-app notifications, one row per recipient.
-- Title, body and link are rendered when the row is written. waiting marks
-- a task (a pending group, a deletion request): it stays in the badge until
-- resolved_at is set, whatever its read state. resolved_by_id and
-- resolution ("approved", "rejected", ...) let other admins see who did it.
CREATE TABLE notifications (
    id bigserial PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind text NOT NULL,
    category text NOT NULL CHECK (category IN ('admin', 'groups', 'events', 'messages', 'safety')),
    priority bigint NOT NULL DEFAULT 0,
    waiting boolean NOT NULL DEFAULT false,
    actor_id bigint,
    subject_type text NOT NULL DEFAULT '',
    subject_id bigint,
    group_key text,
    dedupe_key text,
    title text NOT NULL,
    body text NOT NULL DEFAULT '',
    link text NOT NULL CHECK (link LIKE '/%' AND link NOT LIKE '//%'),
    actions jsonb NOT NULL DEFAULT '[]',
    batch_count bigint NOT NULL DEFAULT 1,
    data jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    seen_at timestamptz,
    read_at timestamptz,
    archived_at timestamptz,
    resolved_at timestamptz,
    resolved_by_id bigint,
    resolution text NOT NULL DEFAULT '',
    expires_at timestamptz
);
CREATE UNIQUE INDEX idx_notifications_dedupe ON notifications (user_id, dedupe_key) WHERE dedupe_key IS NOT NULL;
CREATE INDEX idx_notifications_feed ON notifications (user_id, archived_at, updated_at DESC, id DESC);
CREATE INDEX idx_notifications_badge ON notifications (user_id)
    WHERE archived_at IS NULL AND (read_at IS NULL OR (waiting AND resolved_at IS NULL));
CREATE INDEX idx_notifications_batch ON notifications (user_id, group_key)
    WHERE read_at IS NULL AND archived_at IS NULL AND group_key IS NOT NULL;
CREATE INDEX idx_notifications_subject ON notifications (kind, subject_id) WHERE resolved_at IS NULL AND waiting;

-- Carry today's open admin tasks over, so nothing waiting is lost. The
-- text matches internal/notify's group.pending and group.deletion kinds.
INSERT INTO notifications (user_id, kind, category, waiting, actor_id, subject_type, subject_id, dedupe_key, title, body, link, actions, data)
SELECT a.id, 'group.pending', 'admin', true, g.leader_id, 'group', g.id, 'group.pending:' || g.id,
       coalesce(nullif(l.display_name, ''), nullif(l.name, ''), 'Member') || ' started a group: ' || g.name,
       'It needs an admin''s approval before people can find it.',
       '/admin?tab=approvals', '[{"key":"approve","label":"Approve"}]', jsonb_build_object('groupId', g.id)
FROM groups g
JOIN users a ON a.role = 'admin' AND a.status = 'active' AND a.deleted_at IS NULL
LEFT JOIN users l ON l.id = g.leader_id
WHERE g.status = 'pending' AND g.deleted_at IS NULL;

INSERT INTO notifications (user_id, kind, category, waiting, actor_id, subject_type, subject_id, title, body, link, data)
SELECT a.id, 'group.deletion', 'admin', true, g.leader_id, 'group', g.id,
       coalesce(nullif(l.display_name, ''), nullif(l.name, ''), 'Member') || ' asked to delete ' || g.name,
       'They are its only member. Delete the group or keep it.',
       '/admin?tab=approvals', jsonb_build_object('groupId', g.id)
FROM groups g
JOIN users a ON a.role = 'admin' AND a.status = 'active' AND a.deleted_at IS NULL
LEFT JOIN users l ON l.id = g.leader_id
WHERE g.deletion_requested AND g.deleted_at IS NULL;

-- +goose Down
DROP TABLE notifications;
