-- +goose Up
-- M7.4 R4-2d2, owner decision 8: retain address-level optional-email choices
-- under PLAN 10.1. This DEC-138 additive step creates a dormant table only;
-- it takes no lock on an existing table. Hashes are computed only in Go with
-- mail.AddressHash. They are pseudonymous and guessable, not anonymous.
CREATE TABLE email_address_optouts (
    address_hash text NOT NULL CONSTRAINT email_address_optouts_hash_check
        CHECK (address_hash ~ '^[0-9a-f]{64}$'),
    scope text NOT NULL CONSTRAINT email_address_optouts_scope_check
        CHECK (scope IN ('category:admin', 'category:groups', 'category:events', 'category:messages')),
    opted_out_at timestamptz NOT NULL,
    PRIMARY KEY (address_hash, scope)
);

-- +goose Down
-- Scratch databases and the down/up test only. Retained PLAN 10.1 choices
-- must never be silently discarded by a rollback.
LOCK TABLE email_address_optouts IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM email_address_optouts) THEN
        RAISE EXCEPTION 'email_address_optouts has retained opt-outs; restore the upgrade.sh dump instead';
    END IF;
END;
$$;
-- +goose StatementEnd
DROP TABLE email_address_optouts;
