-- Slice 2, milestone 4b review: what a calendar app shows about a date can
-- change under the same UID. sequence goes up, and updated_at is set, in the
-- same write as every such change (its times, cancelling or bringing back,
-- its visibility, or the event's title, description, place, video link or
-- visibility), so the feed and the one-date download say which copy is
-- newer (RFC 5545 SEQUENCE and LAST-MODIFIED).
ALTER TABLE dates ADD COLUMN sequence INTEGER NOT NULL DEFAULT 0;
ALTER TABLE dates ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0;
UPDATE dates SET updated_at = CAST(strftime('%s', 'now') AS INTEGER);
