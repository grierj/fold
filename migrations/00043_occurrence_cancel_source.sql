-- +goose Up
-- Why a date was cancelled. A series edit that drops a date marks it
-- 'series' so a later edit whose rule includes the date again can bring it
-- back; a person cancelling one date marks it 'manual'. Rows cancelled before
-- this column keep '' and are treated like manual (never auto-restored).
ALTER TABLE event_occurrences
    ADD COLUMN cancel_source text NOT NULL DEFAULT '',
    ADD CONSTRAINT event_occurrences_cancel_source_check CHECK (cancel_source IN ('', 'series', 'manual'));

-- +goose Down
ALTER TABLE event_occurrences
    DROP CONSTRAINT event_occurrences_cancel_source_check,
    DROP COLUMN cancel_source;
