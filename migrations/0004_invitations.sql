-- R32: adding someone to a group, or inviting them by email, is an
-- invitation they accept. Nothing about the invited person changes until
-- they do, so an invitation reveals nothing about whether an address has an
-- account.
--
-- By name: person_id is the invited person, who answers from their notice.
-- By email: name and email are what the inviter typed, and the emailed link
-- (stored as token_hash, lasting 24 hours) opens a page where the person
-- says they are an adult and accepts.
CREATE TABLE invitations (
  id         INTEGER PRIMARY KEY,
  uid        TEXT    NOT NULL UNIQUE,  -- random: the id in URLs
  inviter_id INTEGER NOT NULL REFERENCES people(id) ON DELETE CASCADE,
  group_id   INTEGER REFERENCES groups(id),
  person_id  INTEGER REFERENCES people(id) ON DELETE CASCADE,
  name       TEXT,                     -- personal: as the inviter typed it
  email      TEXT,                     -- personal; lower case
  token_hash BLOB    UNIQUE,
  state      TEXT    NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'accepted', 'declined', 'cancelled')),
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  decided_at INTEGER,
  CHECK ((person_id IS NOT NULL) <> (email IS NOT NULL))
) STRICT;
CREATE INDEX invitations_person ON invitations(person_id, state);
CREATE INDEX invitations_group ON invitations(group_id, state);
