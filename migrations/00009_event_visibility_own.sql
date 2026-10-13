-- +goose Up
-- DEC-48: an event's visibility is its own value, copied from the group when
-- the event is created. visibility_source (group|custom) and the live sync
-- with the group go away. leader_opened marks a group event a leader or
-- admin deliberately set wider than its group.

ALTER TABLE events ADD COLUMN leader_opened boolean NOT NULL DEFAULT false;

-- Events that followed the group take the group's current value (the stored
-- column could be a step behind).
UPDATE events AS e
SET visibility = g.visibility
FROM groups AS g
WHERE g.id = e.group_id AND e.visibility_source = 'group';

-- Custom events wider than their group were opened on purpose.
UPDATE events AS e
SET leader_opened = true
FROM groups AS g
WHERE g.id = e.group_id
  AND e.visibility_source = 'custom'
  AND (CASE e.visibility WHEN 'public' THEN 2 WHEN 'private' THEN 1 ELSE 0 END)
    > (CASE g.visibility WHEN 'public' THEN 2 WHEN 'private' THEN 1 ELSE 0 END);

ALTER TABLE events DROP CONSTRAINT events_churchwide_check;
ALTER TABLE events DROP CONSTRAINT events_visibility_source_check;
ALTER TABLE events DROP COLUMN visibility_source;
ALTER TABLE events ADD CONSTRAINT events_churchwide_check CHECK (
    group_id IS NOT NULL
    OR (visibility IN ('public', 'private') AND NOT leader_opened)
);

-- +goose Down
-- An event whose value matches its group and wasn't leader-opened goes
-- back to following the group; everything else (church-wide, leader-opened,
-- or stricter than the group) becomes custom.
ALTER TABLE events ADD COLUMN visibility_source text NOT NULL DEFAULT 'group';
UPDATE events AS e
SET visibility_source = 'custom'
WHERE e.group_id IS NULL
   OR e.leader_opened
   OR e.visibility <> (SELECT g.visibility FROM groups AS g WHERE g.id = e.group_id);

ALTER TABLE events DROP CONSTRAINT events_churchwide_check;
ALTER TABLE events ADD CONSTRAINT events_visibility_source_check
    CHECK (visibility_source IN ('group', 'custom'));
ALTER TABLE events ADD CONSTRAINT events_churchwide_check CHECK (
    group_id IS NOT NULL
    OR (visibility IN ('public', 'private') AND visibility_source = 'custom')
);
ALTER TABLE events DROP COLUMN leader_opened;
