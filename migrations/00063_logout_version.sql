-- +goose Up
-- Logout signs the account out on every device (M7.2 slice b). It bumps
-- logout_version, which only the session cookie check reads, rather than
-- session_version: session_version is the credential epoch (password,
-- email, role, status). Emailed links bind it (guardian consent) and a
-- trigger ends push devices when it moves (00059), and neither should
-- happen at an ordinary sign-out.
ALTER TABLE users ADD COLUMN logout_version bigint NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE users DROP COLUMN logout_version;
