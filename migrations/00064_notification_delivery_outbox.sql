-- +goose Up
-- R6 outbox: updated_at is the retry and outcome bookkeeping clock that
-- the later admin "failed this week" count filters on. Existing rows take
-- COALESCE(sent_at, created_at). The queued-only partial index on created_at
-- serves the reconcile scan of the oldest queued rows (a later R6 slice), the
-- queued count and the oldest-queued value; it does not serve failed counts.
-- Fold is pre-live, so the single-shot ADD COLUMN/UPDATE/CREATE INDEX is
-- fine; the stable now() default needs no table rewrite. Once live, a change
-- like this needs a batched backfill and CREATE INDEX CONCURRENTLY.
ALTER TABLE notification_deliveries ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();
UPDATE notification_deliveries SET updated_at = COALESCE(sent_at, created_at);
CREATE INDEX idx_notification_deliveries_queued ON notification_deliveries (created_at) WHERE status = 'queued';

-- +goose Down
DROP INDEX idx_notification_deliveries_queued;
ALTER TABLE notification_deliveries DROP COLUMN updated_at;
