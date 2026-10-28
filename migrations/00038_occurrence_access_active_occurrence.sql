-- +goose Up
-- Restrict attendee provenance reads to the dates on the current page rather
-- than hashing all active access history on every group or event request.
CREATE INDEX occurrence_accesses_active_occurrence
    ON occurrence_accesses (occurrence_id) WHERE status = 'active';

-- +goose Down
DROP INDEX occurrence_accesses_active_occurrence;
