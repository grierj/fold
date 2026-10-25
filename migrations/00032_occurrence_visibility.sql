-- +goose Up
ALTER TABLE event_occurrences ADD COLUMN visibility_override text
    CHECK (visibility_override IN ('public', 'private', 'invite_only'));

-- +goose Down
ALTER TABLE event_occurrences DROP COLUMN visibility_override;
