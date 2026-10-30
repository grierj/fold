-- +goose Up
CREATE INDEX idx_memberships_group_id ON memberships (group_id);

-- +goose Down
DROP INDEX idx_memberships_group_id;
