-- +goose Up
-- Push's hourly limit (M3.13d1, DEC-126): before an optional push, Fold
-- counts the person's push rows sent in the last hour. One row per
-- notification, whatever the device count, so this counts notifications.
-- Partial, so it holds only sent pushes.
CREATE INDEX idx_notification_deliveries_push_sent ON notification_deliveries (user_id, sent_at)
    WHERE channel = 'push' AND status = 'sent';

-- +goose Down
DROP INDEX idx_notification_deliveries_push_sent;
