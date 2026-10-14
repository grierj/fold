-- +goose Up
-- M1.3 (C15, 5.3, 5.10, DEC-4, DEC-8): self-signup account requests and
-- the age gate. users gain account_type and birth_date; account_requests
-- holds every pending request an admin decides. Approval creates the
-- User and emails an account_setup token link (24 hours, 10.1), so
-- account_tokens learns that kind.

ALTER TABLE users ADD COLUMN account_type text NOT NULL DEFAULT 'adult'
    CONSTRAINT users_account_type_check CHECK (account_type IN ('adult', 'teen'));
ALTER TABLE users ADD COLUMN birth_date date;

CREATE TABLE account_requests (
    id bigserial PRIMARY KEY,
    name text NOT NULL,
    email citext NOT NULL,
    phone text NOT NULL DEFAULT '',
    account_type text NOT NULL DEFAULT 'adult' CHECK (account_type IN ('adult', 'teen')),
    birth_date date,
    message text NOT NULL DEFAULT '',
    source text NOT NULL CHECK (source IN ('self_signup', 'member_invite', 'guest_upgrade')),
    requested_by bigint REFERENCES users (id) ON DELETE SET NULL,
    group_id bigint REFERENCES groups (id) ON DELETE SET NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'expired', 'withdrawn')),
    decided_by bigint,
    decided_at timestamptz,
    decision_note text NOT NULL DEFAULT '',
    flags jsonb NOT NULL DEFAULT '{}',
    ip text NOT NULL DEFAULT '',
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_account_requests_email ON account_requests (email);
CREATE INDEX idx_account_requests_status ON account_requests (status);
CREATE INDEX idx_account_requests_expires_at ON account_requests (expires_at);
CREATE INDEX idx_account_requests_requested_by ON account_requests (requested_by);
CREATE INDEX idx_account_requests_group_id ON account_requests (group_id);

-- account_setup: the set-password token emailed when a request is
-- approved. The old CHECK is found by its definition so this works
-- whatever name Postgres gave it in 00011.
-- +goose StatementBegin
DO $$
DECLARE c text;
BEGIN
    SELECT conname INTO c FROM pg_constraint
    WHERE conrelid = 'account_tokens'::regclass AND contype = 'c'
      AND pg_get_constraintdef(oid) LIKE '%kind%';
    IF c IS NOT NULL THEN
        EXECUTE 'ALTER TABLE account_tokens DROP CONSTRAINT ' || quote_ident(c);
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE account_tokens ADD CONSTRAINT account_tokens_kind_check
    CHECK (kind IN ('password_reset', 'email_verify', 'account_setup'));

-- +goose Down
ALTER TABLE account_tokens DROP CONSTRAINT account_tokens_kind_check;
ALTER TABLE account_tokens ADD CONSTRAINT account_tokens_kind_check
    CHECK (kind IN ('password_reset', 'email_verify'));
DROP TABLE account_requests;
ALTER TABLE users DROP COLUMN birth_date;
ALTER TABLE users DROP COLUMN account_type;
