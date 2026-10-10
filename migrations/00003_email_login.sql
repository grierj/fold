-- +goose Up
-- M0.3a: log in by email (C16, DEC-23), account status, and login throttling
-- stored in Postgres (10.1). Clean break (DEC-22): no data is carried over,
-- so an existing row without a unique email makes this fail loudly.

CREATE EXTENSION IF NOT EXISTS citext;

ALTER TABLE users DROP COLUMN username;
ALTER TABLE users ALTER COLUMN email TYPE citext;
ALTER TABLE users ALTER COLUMN email SET NOT NULL;
ALTER TABLE users ADD CONSTRAINT uni_users_email UNIQUE (email);

ALTER TABLE users RENAME COLUMN password TO password_hash;
ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL;

ALTER TABLE users ADD COLUMN status text NOT NULL DEFAULT 'active'
    CONSTRAINT users_status_check
    CHECK (status IN ('active', 'invited', 'guest', 'pending_consent', 'disabled'));

CREATE TABLE login_throttles (
    key text PRIMARY KEY,
    failures bigint NOT NULL DEFAULT 0,
    window_start timestamptz NOT NULL,
    locked_until timestamptz
);

-- +goose Down
DROP TABLE login_throttles;
ALTER TABLE users DROP COLUMN status;
ALTER TABLE users RENAME COLUMN password_hash TO password;
ALTER TABLE users DROP CONSTRAINT uni_users_email;
ALTER TABLE users ALTER COLUMN email DROP NOT NULL;
ALTER TABLE users ALTER COLUMN email TYPE text;
ALTER TABLE users ADD COLUMN username text;
