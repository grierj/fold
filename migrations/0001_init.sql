-- Slice 0. Every table is STRICT (docs/HAZARDS.md section 9). Times are
-- integer seconds since the Unix epoch, in UTC.
--
-- Personal data lives only in the columns marked "personal".

CREATE TABLE people (
  id         INTEGER PRIMARY KEY,
  uid        TEXT    NOT NULL UNIQUE,  -- random, never reused, the only id the audit log holds
  name       TEXT    NOT NULL,         -- personal
  email      TEXT    NOT NULL UNIQUE,  -- personal; lower case
  status     TEXT    NOT NULL CHECK (status IN ('setup', 'active', 'disabled')),
  is_admin   INTEGER NOT NULL DEFAULT 0 CHECK (is_admin IN (0, 1)),
  zone       TEXT    NOT NULL DEFAULT '',
  version    INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE sessions (
  id         INTEGER PRIMARY KEY,
  token_hash BLOB    NOT NULL UNIQUE,
  person_id  INTEGER NOT NULL REFERENCES people(id),
  created_at INTEGER NOT NULL,
  renewed_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  ended_at   INTEGER
) STRICT;
CREATE INDEX sessions_open ON sessions(person_id) WHERE ended_at IS NULL;

-- A link is single use. Only its hash is stored; the token itself exists
-- sealed in the job that emails it, until that job ends.
CREATE TABLE links (
  id           INTEGER PRIMARY KEY,
  purpose      TEXT    NOT NULL CHECK (purpose IN ('setup', 'signin')),
  person_id    INTEGER NOT NULL REFERENCES people(id),
  token_hash   BLOB    NOT NULL UNIQUE,
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  used_at      INTEGER,
  cancelled_at INTEGER
) STRICT;
CREATE INDEX links_person ON links(person_id, purpose);

-- Values whose key ends in ".sealed" are encrypted (internal/keys).
CREATE TABLE settings (
  key        TEXT    PRIMARY KEY,
  value      TEXT    NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE jobs (
  id          INTEGER PRIMARY KEY,
  kind        TEXT    NOT NULL,
  ref         INTEGER NOT NULL DEFAULT 0,
  secret      BLOB,                       -- sealed; set to NULL when the job ends
  unique_key  TEXT    UNIQUE,
  run_at      INTEGER NOT NULL,
  attempts    INTEGER NOT NULL DEFAULT 0,
  last_error  TEXT    NOT NULL DEFAULT '', -- addresses are scrubbed before storing
  state       TEXT    NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'done', 'failed', 'cancelled')),
  created_at  INTEGER NOT NULL,
  finished_at INTEGER
) STRICT;
CREATE INDEX jobs_due ON jobs(run_at) WHERE state = 'pending';
CREATE INDEX jobs_ref ON jobs(kind, ref);

-- The sealed audit log (docs/DECISIONS.md R6, P17). Rows hold ids and short
-- structured values only, never a name, an address or free text. No page
-- reads this table.
CREATE TABLE audit_log (
  seq       INTEGER PRIMARY KEY,
  at        INTEGER NOT NULL,
  action    TEXT    NOT NULL,
  actor     TEXT    NOT NULL,
  subjects  TEXT    NOT NULL,  -- JSON array of random identifiers
  details   TEXT    NOT NULL,  -- JSON object of short structured values
  format    INTEGER NOT NULL,
  prev_hash BLOB    NOT NULL,
  hash      BLOB    NOT NULL
) STRICT;

CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log
BEGIN
  SELECT RAISE(ABORT, 'the audit log is sealed');
END;

CREATE TRIGGER audit_log_no_delete BEFORE DELETE ON audit_log
BEGIN
  SELECT RAISE(ABORT, 'the audit log is sealed');
END;

-- A row may only be added at the end, after the row it names as its
-- predecessor. This also refuses INSERT OR REPLACE over an existing row.
CREATE TRIGGER audit_log_append_only BEFORE INSERT ON audit_log
WHEN NEW.seq IS NOT (SELECT coalesce(max(seq), 0) + 1 FROM audit_log)
  OR NEW.prev_hash IS NOT coalesce((SELECT hash FROM audit_log ORDER BY seq DESC LIMIT 1), zeroblob(32))
BEGIN
  SELECT RAISE(ABORT, 'the audit log only grows at its end');
END;
