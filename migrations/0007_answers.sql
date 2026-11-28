-- Slice 2, milestone 2: answers to dates, and an event's video link.

-- One answer per person per date. Going, Maybe and Can't go are a person's
-- own (DEC-63); cancelled is the system taking an answer back, as when the
-- person leaves the group, and is history, never counted (DEC-54). A
-- disabled account's answers stay, hidden and not counted (DEC-38). Every
-- change goes through package answers.
--
-- 'waiting' and waiting_since are for the waitlist (milestone 2b) and are
-- unused until then; a CHECK cannot be widened later without rebuilding the
-- table.
CREATE TABLE answers (
  id            INTEGER PRIMARY KEY,
  date_id       INTEGER NOT NULL REFERENCES dates(id),
  person_id     INTEGER NOT NULL REFERENCES people(id),
  status        TEXT    NOT NULL CHECK (status IN ('going', 'maybe', 'cant', 'cancelled', 'waiting')),
  waiting_since INTEGER,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  UNIQUE (date_id, person_id)
) STRICT;
CREATE INDEX answers_person ON answers(person_id);

-- The one definition of an answer that counts: a person's own answer, from
-- an active account. Every count and name list reads this.
CREATE VIEW counted_answers AS
  SELECT a.id, a.date_id, a.person_id, a.status
  FROM answers a JOIN people p ON p.id = a.person_id
  WHERE a.status IN ('going', 'maybe', 'cant') AND p.status = 'active';

-- An optional https link out to a video call, shown only to people who said
-- Going or Maybe and to those running the event (R41, DEC-16). Never fetched.
ALTER TABLE events ADD COLUMN video_url TEXT NOT NULL DEFAULT ''; -- text
