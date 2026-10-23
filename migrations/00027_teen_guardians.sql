-- +goose Up
ALTER TABLE account_requests ADD COLUMN guardian_email text NOT NULL DEFAULT '';
ALTER TABLE account_requests ADD COLUMN approved_user_id bigint REFERENCES users(id) ON DELETE SET NULL;
UPDATE account_requests r SET approved_user_id = u.id FROM users u WHERE r.status = 'approved' AND r.email = u.email;
ALTER TABLE account_tokens DROP CONSTRAINT account_tokens_kind_check;
ALTER TABLE account_tokens ADD CONSTRAINT account_tokens_kind_check
    CHECK (kind IN ('password_reset', 'email_verify', 'account_setup', 'teen_consent'));
CREATE UNIQUE INDEX guardian_links_live_teen_guardian ON guardian_links (guardian_user_id, teen_user_id)
    WHERE teen_user_id IS NOT NULL AND status IN ('pending', 'confirmed');

-- +goose Down
DROP INDEX guardian_links_live_teen_guardian;
DELETE FROM account_tokens WHERE kind = 'teen_consent';
ALTER TABLE account_tokens DROP CONSTRAINT account_tokens_kind_check;
ALTER TABLE account_tokens ADD CONSTRAINT account_tokens_kind_check
    CHECK (kind IN ('password_reset', 'email_verify', 'account_setup'));
ALTER TABLE account_requests DROP COLUMN guardian_email;
ALTER TABLE account_requests DROP COLUMN approved_user_id;
