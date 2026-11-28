-- Slice 2, milestone 3a: guests.

-- people gains the 'guest' status: someone who answered a public date by
-- emailed link and is not a member (R15, R35). A guest asking for an
-- account keeps their status and answers until an admin decides, so the
-- ask is requested_at. SQLite cannot change a CHECK constraint in place, so
-- the table is rebuilt, as 0002 did; the views reading people are dropped
-- first and made again after. The runner turns foreign keys off for this
-- and checks them all before committing.
DROP VIEW counted_answers;
DROP VIEW active_memberships;

CREATE TABLE people_new (
  id             INTEGER PRIMARY KEY,
  uid            TEXT    NOT NULL UNIQUE,  -- random, never reused, the only id the audit log holds
  name           TEXT    NOT NULL,         -- personal
  email          TEXT    NOT NULL UNIQUE,  -- personal; lower case
  status         TEXT    NOT NULL CHECK (status IN ('setup', 'pending', 'active', 'disabled', 'guest')),
  is_admin       INTEGER NOT NULL DEFAULT 0 CHECK (is_admin IN (0, 1)),
  zone           TEXT    NOT NULL DEFAULT '',
  contact_hidden INTEGER NOT NULL DEFAULT 0 CHECK (contact_hidden IN (0, 1)),
  requested_at   INTEGER,                  -- a guest asked for an account
  version        INTEGER NOT NULL DEFAULT 1,
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL,
  CHECK (requested_at IS NULL OR status = 'guest')
) STRICT;
INSERT INTO people_new (id, uid, name, email, status, is_admin, zone, contact_hidden, version, created_at, updated_at)
  SELECT id, uid, name, email, status, is_admin, zone, contact_hidden, version, created_at, updated_at FROM people;
DROP TABLE people;
ALTER TABLE people_new RENAME TO people;

-- Every emailed secret is a row here, so restoring a backup (which cancels
-- them all) and turning an account off (which cancels its own) reach the
-- guest links too.
--   guest:  the confirm link (R35). No person exists yet: the name and
--           email wait sealed (keys.Guest), with a keyed MAC of the inbox
--           to cap and replace them, and the row is deleted when used or
--           expired.
--   manage: a standing link to one date for the person who confirmed
--           (DEC-55), cancelled when they cancel and dead once it ends.
CREATE TABLE links_new (
  id           INTEGER PRIMARY KEY,
  purpose      TEXT    NOT NULL CHECK (purpose IN ('setup', 'signin', 'guest', 'manage')),
  person_id    INTEGER REFERENCES people(id),
  date_id      INTEGER REFERENCES dates(id),
  sealed       BLOB,                     -- personal, sealed: a guest's name and email
  address_mac  BLOB,
  token_hash   BLOB    NOT NULL UNIQUE,
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  used_at      INTEGER,
  cancelled_at INTEGER,
  CHECK ((purpose = 'guest') = (person_id IS NULL)),
  CHECK ((purpose = 'guest') = (sealed IS NOT NULL AND address_mac IS NOT NULL)),
  CHECK ((purpose IN ('guest', 'manage')) = (date_id IS NOT NULL))
) STRICT;
INSERT INTO links_new (id, purpose, person_id, token_hash, created_at, expires_at, used_at, cancelled_at)
  SELECT id, purpose, person_id, token_hash, created_at, expires_at, used_at, cancelled_at FROM links;
DROP TABLE links;
ALTER TABLE links_new RENAME TO links;
CREATE INDEX links_person ON links(person_id, purpose);
CREATE INDEX links_address ON links(address_mac) WHERE address_mac IS NOT NULL;

CREATE VIEW active_memberships AS
  SELECT m.id, m.group_id, m.person_id, m.role, m.created_at
  FROM memberships m
  JOIN people p ON p.id = m.person_id
  JOIN groups g ON g.id = m.group_id
  WHERE m.state = 'active' AND p.status = 'active' AND g.status <> 'deleted';

-- A guest's answer counts like a member's.
CREATE VIEW counted_answers AS
  SELECT a.id, a.date_id, a.person_id, a.status, a.waiting_since
  FROM answers a JOIN people p ON p.id = a.person_id
  WHERE a.status IN ('going', 'maybe', 'cant', 'waiting') AND p.status IN ('active', 'guest');
