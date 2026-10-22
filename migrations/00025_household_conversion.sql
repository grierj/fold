-- +goose Up
ALTER TABLE invitations ADD COLUMN household_person_id bigint REFERENCES household_people(id);
ALTER TABLE invitations ADD COLUMN revoked_at timestamptz;
ALTER TABLE invitations ADD CONSTRAINT invitations_conversion CHECK (
    household_person_id IS NULL OR (group_id IS NULL AND NOT over_limit AND role = 'member')
);
CREATE UNIQUE INDEX idx_invitations_open_conversion ON invitations(household_person_id)
    WHERE household_person_id IS NOT NULL AND accepted_at IS NULL AND revoked_at IS NULL;
-- Converted adults wait for admin approval in the account-request queue (DEC-4).
ALTER TABLE account_requests DROP CONSTRAINT account_requests_source_check;
ALTER TABLE account_requests ADD CONSTRAINT account_requests_source_check
    CHECK (source IN ('self_signup', 'member_invite', 'guest_upgrade', 'household_conversion'));

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM invitations WHERE household_person_id IS NOT NULL)
        OR EXISTS (SELECT 1 FROM account_requests WHERE source = 'household_conversion') THEN
        RAISE EXCEPTION 'conversion invitations exist; restore a pre-upgrade backup to roll back';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE account_requests DROP CONSTRAINT account_requests_source_check;
ALTER TABLE account_requests ADD CONSTRAINT account_requests_source_check
    CHECK (source IN ('self_signup', 'member_invite', 'guest_upgrade'));
DROP INDEX idx_invitations_open_conversion;
ALTER TABLE invitations DROP CONSTRAINT invitations_conversion;
ALTER TABLE invitations DROP COLUMN revoked_at;
ALTER TABLE invitations DROP COLUMN household_person_id;
