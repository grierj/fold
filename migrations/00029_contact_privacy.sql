-- +goose Up
ALTER TABLE users ADD COLUMN hide_contact_from_leaders boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE users DROP COLUMN hide_contact_from_leaders;
