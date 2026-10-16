-- +goose Up
-- DEC-58: each person's own time zone, for emails and server-written
-- notification text. '' means "use the church's zone"; the SPA fills it in
-- from the browser the first time the person signs in. A guest's zone comes
-- from the guest form's browser.
ALTER TABLE users ADD COLUMN timezone text NOT NULL DEFAULT '';
ALTER TABLE guest_signups ADD COLUMN timezone text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE guest_signups DROP COLUMN timezone;
ALTER TABLE users DROP COLUMN timezone;
