-- +goose Up
-- The dev outbox keeps an email's HTML part too (M3.4b), shown on its own
-- page at /dev/outbox/<id>/html.
ALTER TABLE mail_outboxes ADD COLUMN html_body text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE mail_outboxes DROP COLUMN html_body;
