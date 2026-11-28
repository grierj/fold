-- Slice 2, milestone 4b: a person's calendar feed, a standing link a phone
-- subscribes to. Only the token's hash is kept, one per person; making a new
-- link replaces it, and turning it off, turning the account off or restoring
-- a backup clears it.
ALTER TABLE people ADD COLUMN feed_hash BLOB;
CREATE UNIQUE INDEX people_feed ON people(feed_hash) WHERE feed_hash IS NOT NULL;
