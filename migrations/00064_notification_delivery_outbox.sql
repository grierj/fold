-- +goose Up
-- R6 outbox: updated_at is the retry bookkeeping clock and the admin
-- "failed this week" count. Existing rows take the send time, or the
-- row's creation when nothing was sent. The partial index serves the
-- reconcile scan and those admin counts; it holds only queued rows.
ALTER TABLE notification_deliveries ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();
UPDATE notification_deliveries SET updated_at = COALESCE(sent_at, created_at);
CREATE INDEX idx_notification_deliveries_queued ON notification_deliveries (created_at) WHERE status = 'queued';

-- +goose Down
DROP INDEX idx_notification_deliveries_queued;
ALTER TABLE notification_deliveries DROP COLUMN updated_at;
