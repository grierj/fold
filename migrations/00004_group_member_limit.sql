-- +goose Up
-- DEC-39: an optional member limit for groups (rooms have a capacity).
-- NULL means unlimited. Only active memberships of listed accounts count.
ALTER TABLE groups ADD COLUMN member_limit bigint
    CONSTRAINT groups_member_limit_check CHECK (member_limit > 0);

-- +goose Down
ALTER TABLE groups DROP COLUMN member_limit;
