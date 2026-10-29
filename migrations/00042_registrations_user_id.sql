-- +goose Up
CREATE INDEX idx_registrations_user_id ON registrations (user_id);

-- +goose Down
DROP INDEX idx_registrations_user_id;
