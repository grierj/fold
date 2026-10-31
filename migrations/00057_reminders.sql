-- +goose Up
-- Email reminders (M3.6, PLAN 7.7, DEC-133). Each event says how long before
-- each date its reminder goes: 0 is off; else 2 hours, 1 day (the
-- default), 2 days or 1 week, in minutes. A reminder is a message
-- (category reminder) with no author, sent through the M3.5 pipeline:
-- occurrence_id ties it to its date, and reminder_key keeps it to one
-- per date, time and kind however often the sweep runs.
ALTER TABLE events ADD COLUMN reminder_email_minutes bigint NOT NULL DEFAULT 1440
    CHECK (reminder_email_minutes IN (0, 120, 1440, 2880, 10080));
ALTER TABLE messages ADD COLUMN occurrence_id bigint REFERENCES event_occurrences(id) ON DELETE SET NULL;
ALTER TABLE messages ADD COLUMN reminder_key text CHECK (length(reminder_key) <= 200);
CREATE UNIQUE INDEX idx_messages_reminder_key ON messages (reminder_key) WHERE reminder_key IS NOT NULL;
CREATE INDEX idx_messages_occurrence ON messages (occurrence_id) WHERE occurrence_id IS NOT NULL;

-- +goose Down
DROP INDEX idx_messages_occurrence;
DROP INDEX idx_messages_reminder_key;
ALTER TABLE messages DROP COLUMN reminder_key;
ALTER TABLE messages DROP COLUMN occurrence_id;
ALTER TABLE events DROP COLUMN reminder_email_minutes;
