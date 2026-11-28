-- Slice 2, milestone 1b: hosts of church-wide events, opening an event or
-- one date wider than its group, and notices about events.

-- The hosts of a church-wide event get a leader's rights over that event
-- only (DEC-7). A row on a group's event grants nothing.
CREATE TABLE event_hosts (
  event_id   INTEGER NOT NULL REFERENCES events(id),
  person_id  INTEGER NOT NULL REFERENCES people(id),
  created_at INTEGER NOT NULL,
  PRIMARY KEY (event_id, person_id)
) STRICT;
CREATE INDEX event_hosts_person ON event_hosts(person_id);

-- Why an event, or one date, was opened wider than its group: optional, set
-- when it is opened and cleared when it is no wider than the group again.
-- It is about the event, not a person, is shown to the group's other leaders
-- and the admins, and is kept in the audit log (DEC-28, R30, R31).
ALTER TABLE events ADD COLUMN open_reason TEXT NOT NULL DEFAULT ''; -- text
ALTER TABLE dates ADD COLUMN open_reason TEXT NOT NULL DEFAULT '';  -- text

-- Notices about an event, or one of its dates.
ALTER TABLE notices ADD COLUMN event_id INTEGER REFERENCES events(id);
ALTER TABLE notices ADD COLUMN date_id INTEGER REFERENCES dates(id);
