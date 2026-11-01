-- +goose Up
-- Push's hourly limit (M3.13d1, DEC-126): before an optional push, Fold
-- counts the person's optional push rows sent in the last hour. One row
-- per notification, whatever the device count, so this counts
-- notifications. A sent row with no reason is an optional push; required
-- pushes and urgent date changes say why they went at once and aren't
-- counted. Partial, so it holds only those rows; the query writes the same
-- predicate as literals so even a cached generic plan can use it.
CREATE INDEX idx_notification_deliveries_push_sent ON notification_deliveries (user_id, sent_at)
    WHERE channel = 'push' AND status = 'sent' AND reason = '';

-- +goose Down
DROP INDEX idx_notification_deliveries_push_sent;
