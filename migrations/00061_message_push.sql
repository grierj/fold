-- +goose Up
-- "Push notification too" on announcements (M3.13d2, DEC-123). An
-- announcement always goes by email; push_alert (default on) adds a short
-- companion notification for the people who get the email and have push
-- on. Each of them gets one message_recipients row with channel 'push',
-- whatever their device count; one push.deliver job per device sends it.
ALTER TABLE messages ADD COLUMN push_alert boolean NOT NULL DEFAULT true;
ALTER TABLE message_recipients DROP CONSTRAINT message_recipients_channel_check;
ALTER TABLE message_recipients ADD CONSTRAINT message_recipients_channel_check CHECK (channel IN ('email', 'sms', 'push'));

-- Push's hourly limit (DEC-126) counts announcement pushes with
-- notification pushes (00060's index): the person's push rows sent in the
-- last hour. Announcement pushes are always optional, so every sent push
-- row counts. The query writes this predicate as literals, as 00060's does.
CREATE INDEX idx_message_recipients_push_sent ON message_recipients (user_id, sent_at)
    WHERE channel = 'push' AND status = 'sent';
-- The daily reaper finds push rows still queued with no job left to send
-- them (a device added after the snapshot, a job that died). Few rows.
CREATE INDEX idx_message_recipients_push_queued ON message_recipients (updated_at)
    WHERE channel = 'push' AND status = 'queued';

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM message_recipients WHERE channel = 'push') THEN
        RAISE EXCEPTION 'announcement push rows exist; restore a pre-upgrade backup to roll back';
    END IF;
END $$;
-- +goose StatementEnd
DROP INDEX idx_message_recipients_push_queued;
DROP INDEX idx_message_recipients_push_sent;
ALTER TABLE message_recipients DROP CONSTRAINT message_recipients_channel_check;
ALTER TABLE message_recipients ADD CONSTRAINT message_recipients_channel_check CHECK (channel IN ('email', 'sms'));
ALTER TABLE messages DROP COLUMN push_alert;
