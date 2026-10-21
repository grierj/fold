-- +goose Up
-- M1.8 (5.8): video links on events. A leader pastes a link or generates
-- a Jitsi room URL; video_reveal decides who sees it (registered_only by
-- default). There is no embedded video anywhere (DEC-16).

ALTER TABLE events ADD COLUMN video_provider text NOT NULL DEFAULT 'none'
    CONSTRAINT events_video_provider_check
    CHECK (video_provider IN ('none', 'link', 'jitsi'));
ALTER TABLE events ADD COLUMN video_url text NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN video_reveal text NOT NULL DEFAULT 'registered_only'
    CONSTRAINT events_video_reveal_check
    CHECK (video_reveal IN ('always', 'registered_only'));

-- +goose Down
ALTER TABLE events DROP COLUMN video_reveal;
ALTER TABLE events DROP COLUMN video_url;
ALTER TABLE events DROP COLUMN video_provider;
