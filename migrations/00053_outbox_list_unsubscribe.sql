-- +goose Up
-- The devoutbox keeps an email's one-click unsubscribe URL (M3.3), so dev
-- and the browser tests can see what the List-Unsubscribe header carries.
ALTER TABLE mail_outboxes ADD COLUMN list_unsubscribe text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE mail_outboxes DROP COLUMN list_unsubscribe;
