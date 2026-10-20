-- +goose Up
-- #78: the guest manage link looks up registrations by manage_token_hash
-- on a public route; without an index that is a sequential scan of every
-- registration. Tokens are random hashes, so the index is unique; members'
-- rows carry an empty hash and stay out of it.
CREATE UNIQUE INDEX idx_registrations_manage_token ON registrations (manage_token_hash) WHERE manage_token_hash <> '';

-- +goose Down
DROP INDEX idx_registrations_manage_token;
