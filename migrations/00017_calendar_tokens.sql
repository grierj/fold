-- +goose Up
-- M1.7 (8.2): one secret calendar-feed token per person. The raw token
-- is shown once on the profile page and travels only in the feed URL;
-- only its SHA-256 is stored. Regenerating replaces the hash, which
-- revokes the old link. scope is "mine" (Option A) or "all" (Option B).
CREATE TABLE calendar_tokens (
    id bigserial PRIMARY KEY,
    user_id bigint NOT NULL CONSTRAINT fk_calendar_tokens_user REFERENCES users (id) ON DELETE CASCADE,
    token_hash text NOT NULL,
    scope text NOT NULL DEFAULT 'mine' CHECK (scope IN ('mine', 'all')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_calendar_tokens_user_id ON calendar_tokens (user_id);
CREATE UNIQUE INDEX idx_calendar_tokens_token_hash ON calendar_tokens (token_hash);

-- +goose Down
DROP TABLE calendar_tokens;
