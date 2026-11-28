-- Slice 2: events and their dates.
--
-- Three kinds of time, never mixed in one column (docs/HAZARDS.md section 1):
-- an instant is integer seconds in UTC (*_at); a wall-clock time is text
-- YYYY-MM-DDTHH:MM beside the event's IANA zone (*_local); a calendar date is
-- text YYYY-MM-DD (day, until).

-- A repeat is stored as its parts, never as a rule string (R33): once, every
-- week, every other week, or monthly on the Nth weekday of the first date,
-- where month_week is 1 to 4, or -1 for the last (R37). A deleted event stays
-- as a row, marked deleted, and nothing shows it or its dates.
CREATE TABLE events (
  id           INTEGER PRIMARY KEY,
  uid          TEXT    NOT NULL UNIQUE,     -- random: the id in URLs and the audit log
  group_id     INTEGER REFERENCES groups(id), -- NULL is a church-wide event (slice 2, milestone 1b)
  title        TEXT    NOT NULL,            -- text
  about        TEXT    NOT NULL DEFAULT '', -- text
  place        TEXT    NOT NULL DEFAULT '', -- text
  visibility   TEXT    NOT NULL CHECK (visibility IN ('public', 'church', 'invite_only')),
  zone         TEXT    NOT NULL,            -- IANA name; the event's own clock (DEC-57)
  starts_local TEXT    NOT NULL,            -- the first date's wall clock, in zone
  minutes      INTEGER NOT NULL CHECK (minutes BETWEEN 1 AND 10080),
  repeat       TEXT    NOT NULL CHECK (repeat IN ('once', 'weekly', 'biweekly', 'monthly')),
  month_week   INTEGER CHECK (month_week IN (1, 2, 3, 4, -1)),
  until        TEXT,                        -- the last day it may fall on, inclusive
  seats        INTEGER CHECK (seats > 0),   -- no limit when NULL; set from milestone 2
  status       TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'deleted')),
  created_by   INTEGER REFERENCES people(id),
  version      INTEGER NOT NULL DEFAULT 1,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  CHECK ((repeat = 'monthly') = (month_week IS NOT NULL))
) STRICT;
CREATE INDEX events_group ON events(group_id);

-- One row per day an event falls on, stored 18 months ahead and extended
-- nightly. day is the day the repeat puts it on, in the event's zone, and
-- never changes; starts_local is the wall clock it really runs at, which
-- differs only when R34 moved it out of a daylight-saving gap (moved = 1).
-- A date the series no longer makes is cancelled, never deleted, so it can
-- come back (DEC-91).
CREATE TABLE dates (
  id           INTEGER PRIMARY KEY,
  uid          TEXT    NOT NULL UNIQUE,     -- random: the id in URLs
  event_id     INTEGER NOT NULL REFERENCES events(id),
  day          TEXT    NOT NULL,
  starts_at    INTEGER NOT NULL,
  ends_at      INTEGER NOT NULL,
  starts_local TEXT    NOT NULL,
  moved        INTEGER NOT NULL DEFAULT 0 CHECK (moved IN (0, 1)),
  status       TEXT    NOT NULL DEFAULT 'scheduled' CHECK (status IN ('scheduled', 'cancelled')),
  cancelled_by TEXT    CHECK (cancelled_by IN ('series', 'person')),
  visibility   TEXT    CHECK (visibility IN ('public', 'church', 'invite_only')), -- NULL is the event's (P7, milestone 1b)
  UNIQUE (event_id, day),
  CHECK ((status = 'cancelled') = (cancelled_by IS NOT NULL))
) STRICT;
CREATE INDEX dates_starts ON dates(starts_at);
CREATE INDEX dates_event_starts ON dates(event_id, starts_at);
