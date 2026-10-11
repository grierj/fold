-- +goose Up
-- Plain-text description for an event series. Same limit as a group
-- description (5000). Existing rows get an empty string. HTML descriptions
-- stay later; this column is text the page shows as written.

ALTER TABLE events ADD COLUMN description text NOT NULL DEFAULT '';
ALTER TABLE events ALTER COLUMN description DROP DEFAULT;

-- +goose Down
ALTER TABLE events DROP COLUMN description;
