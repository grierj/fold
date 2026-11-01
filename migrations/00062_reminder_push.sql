-- +goose Up
-- The 2-hour push reminder (M3.13d3, PLAN 7.7, DEC-123): on by default
-- for every event, sent to the going registrants who turned on
-- notifications. Each date's push reminder is its own reminder message
-- (reminder_key push:<occurrence>:<start>:120, channel push) holding only
-- push rows.
ALTER TABLE events ADD COLUMN reminder_push boolean NOT NULL DEFAULT true;
ALTER TABLE messages DROP CONSTRAINT messages_channel_check;
ALTER TABLE messages ADD CONSTRAINT messages_channel_check CHECK (channel IN ('email', 'sms', 'both', 'push'));

-- +goose Down
-- Dropping the switch loses only each event's choice (with the code rolled
-- back there is no push reminder to turn off), so it is unguarded. A push
-- reminder message reads as an email one again; its push rows stay.
UPDATE messages SET channel = 'email' WHERE channel = 'push';
ALTER TABLE messages DROP CONSTRAINT messages_channel_check;
ALTER TABLE messages ADD CONSTRAINT messages_channel_check CHECK (channel IN ('email', 'sms', 'both'));
ALTER TABLE events DROP COLUMN reminder_push;
