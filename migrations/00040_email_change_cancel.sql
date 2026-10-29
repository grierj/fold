-- +goose Up
-- Legacy pending email-change links did not require current-account proof, so
-- revoke them before enabling separately authenticated cancellation links.
DELETE FROM account_tokens
WHERE kind = 'email_verify' AND used_at IS NULL;

ALTER TABLE account_tokens DROP CONSTRAINT account_tokens_kind_check;
ALTER TABLE account_tokens ADD CONSTRAINT account_tokens_kind_check
    CHECK (kind IN ('password_reset', 'email_verify', 'account_setup', 'teen_consent', 'email_change_cancel'));

-- +goose Down
-- Revoke cancellation links and pending verification links together so a
-- rollback cannot leave an unpaired pending email change. Used history stays.
DELETE FROM account_tokens
WHERE kind = 'email_change_cancel'
   OR (kind = 'email_verify' AND used_at IS NULL);

ALTER TABLE account_tokens DROP CONSTRAINT account_tokens_kind_check;
ALTER TABLE account_tokens ADD CONSTRAINT account_tokens_kind_check
    CHECK (kind IN ('password_reset', 'email_verify', 'account_setup', 'teen_consent'));
