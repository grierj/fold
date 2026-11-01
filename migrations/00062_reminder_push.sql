-- +goose Up
-- The 2-hour push reminder (M3.13d3, PLAN 7.7, DEC-123): on by default
-- for every event, sent to the going registrants who turned on
-- notifications. Each date's push reminder is its own reminder message
-- (reminder_key push:<occurrence>:<start>:120) holding only push rows.
ALTER TABLE events ADD COLUMN reminder_push boolean NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE events DROP COLUMN reminder_push;
