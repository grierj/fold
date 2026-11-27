-- Slice 1: account requests, groups, memberships and notices.

-- people gains the 'pending' status (an account request waiting for an
-- admin) and the contact choice (P5). SQLite cannot change a CHECK
-- constraint in place, so the table is rebuilt; sessions and links keep
-- pointing at the same ids. The runner turns foreign keys off for this and
-- checks them all before committing (internal/db/migrate.go).

CREATE TABLE people_new (
  id             INTEGER PRIMARY KEY,
  uid            TEXT    NOT NULL UNIQUE,  -- random, never reused, the only id the audit log holds
  name           TEXT    NOT NULL,         -- personal
  email          TEXT    NOT NULL UNIQUE,  -- personal; lower case
  status         TEXT    NOT NULL CHECK (status IN ('setup', 'pending', 'active', 'disabled')),
  is_admin       INTEGER NOT NULL DEFAULT 0 CHECK (is_admin IN (0, 1)),
  zone           TEXT    NOT NULL DEFAULT '',
  contact_hidden INTEGER NOT NULL DEFAULT 0 CHECK (contact_hidden IN (0, 1)), -- P5: leaders do not see the email
  version        INTEGER NOT NULL DEFAULT 1,
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
) STRICT;

INSERT INTO people_new (id, uid, name, email, status, is_admin, zone, version, created_at, updated_at)
  SELECT id, uid, name, email, status, is_admin, zone, version, created_at, updated_at FROM people;
DROP TABLE people;
ALTER TABLE people_new RENAME TO people;

-- A deleted group stays as a row with status 'deleted', so the notices that
-- tell its members can still name it. Nothing shows a deleted group.
-- "text" marks words people write that may mention someone; erasure
-- (slice 4) reviews every column so marked, alongside "personal".
CREATE TABLE groups (
  id         INTEGER PRIMARY KEY,
  uid        TEXT    NOT NULL UNIQUE,  -- random: the id in URLs and the audit log
  name       TEXT    NOT NULL,         -- text
  about      TEXT    NOT NULL DEFAULT '', -- text
  visibility TEXT    NOT NULL CHECK (visibility IN ('public', 'church', 'invite_only')),
  status     TEXT    NOT NULL CHECK (status IN ('pending', 'active', 'archived', 'deleted')),
  version    INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX groups_status ON groups(status);

-- Leadership is a role on a membership. A person who left may join again;
-- a person a leader removed may not (DEC-35, DEC-86).
CREATE TABLE memberships (
  id         INTEGER PRIMARY KEY,
  group_id   INTEGER NOT NULL REFERENCES groups(id),
  person_id  INTEGER NOT NULL REFERENCES people(id),
  role       TEXT    NOT NULL CHECK (role IN ('member', 'leader')),
  state      TEXT    NOT NULL CHECK (state IN ('active', 'left', 'removed')),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE (group_id, person_id)
) STRICT;
CREATE INDEX memberships_person ON memberships(person_id);

-- The one definition of an active membership: the membership is active, so
-- is its person, and its group is not deleted. A disabled account keeps its
-- rows, hidden and not counted (DEC-38); a deleted group keeps its rows so
-- its members can be told. Every count of members or leaders reads this.
CREATE VIEW active_memberships AS
  SELECT m.id, m.group_id, m.person_id, m.role, m.created_at
  FROM memberships m
  JOIN people p ON p.id = m.person_id
  JOIN groups g ON g.id = m.group_id
  WHERE m.state = 'active' AND p.status = 'active' AND g.status <> 'deleted';

-- A notice stores a kind and ids, never words (P12). It is worded when read,
-- through the same visibility rules as any page, so it can never carry an
-- address or a phone number.
CREATE TABLE notices (
  id         INTEGER PRIMARY KEY,
  person_id  INTEGER NOT NULL REFERENCES people(id) ON DELETE CASCADE, -- who it is for
  kind       TEXT    NOT NULL,
  actor_id   INTEGER REFERENCES people(id) ON DELETE SET NULL,         -- who did it
  subject_id INTEGER REFERENCES people(id) ON DELETE CASCADE,          -- the person it is about
  group_id   INTEGER REFERENCES groups(id),
  created_at INTEGER NOT NULL,
  read_at    INTEGER
) STRICT;
CREATE INDEX notices_for ON notices(person_id, read_at);
