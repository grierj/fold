-- +goose Up
-- M1.1b: church-wide events (nullable group_id), visibility, and hosts.
-- Existing rows follow their group (DEC-22 has nothing older to convert).
-- CHECK constraints are extra; GORM does not emit them.

ALTER TABLE events ALTER COLUMN group_id DROP NOT NULL;

ALTER TABLE events ADD COLUMN visibility text;
UPDATE events AS e
SET visibility = g.visibility
FROM groups AS g
WHERE g.id = e.group_id;
UPDATE events SET visibility = 'public' WHERE visibility IS NULL;
ALTER TABLE events ALTER COLUMN visibility SET NOT NULL;
ALTER TABLE events ADD CONSTRAINT events_visibility_check
    CHECK (visibility IN ('public', 'private', 'invite_only'));

ALTER TABLE events ADD COLUMN visibility_source text NOT NULL DEFAULT 'group';
ALTER TABLE events ADD CONSTRAINT events_visibility_source_check
    CHECK (visibility_source IN ('group', 'custom'));
ALTER TABLE events ADD CONSTRAINT events_churchwide_check CHECK (
    group_id IS NOT NULL
    OR (visibility IN ('public', 'private') AND visibility_source = 'custom')
);

CREATE TABLE event_hosts (
    id bigserial PRIMARY KEY,
    event_id bigint NOT NULL CONSTRAINT fk_event_hosts_event REFERENCES events (id),
    user_id bigint NOT NULL CONSTRAINT fk_event_hosts_user REFERENCES users (id),
    added_by bigint NOT NULL,
    created_at timestamptz
);
CREATE UNIQUE INDEX idx_event_host ON event_hosts (event_id, user_id);

-- +goose Down
DROP TABLE event_hosts;

ALTER TABLE events DROP CONSTRAINT events_churchwide_check;
ALTER TABLE events DROP CONSTRAINT events_visibility_source_check;
ALTER TABLE events DROP COLUMN visibility_source;
ALTER TABLE events DROP CONSTRAINT events_visibility_check;
ALTER TABLE events DROP COLUMN visibility;

DELETE FROM registrations
WHERE occurrence_id IN (
    SELECT event_occurrences.id FROM event_occurrences
    JOIN events ON events.id = event_occurrences.event_id
    WHERE events.group_id IS NULL
);
DELETE FROM event_occurrences
WHERE event_id IN (SELECT id FROM events WHERE group_id IS NULL);
DELETE FROM events WHERE group_id IS NULL;
ALTER TABLE events ALTER COLUMN group_id SET NOT NULL;
