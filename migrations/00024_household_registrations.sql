-- +goose Up
ALTER TABLE registrations ALTER COLUMN user_id DROP NOT NULL;
ALTER TABLE registrations
    ADD COLUMN household_person_id bigint REFERENCES household_people(id),
    ADD COLUMN registered_by bigint REFERENCES users(id),
    ADD COLUMN childcare_requested boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT registrations_subject CHECK ((user_id IS NOT NULL) <> (household_person_id IS NOT NULL)),
    ADD CONSTRAINT registrations_holder CHECK ((household_person_id IS NOT NULL AND registered_by IS NOT NULL) OR (user_id IS NOT NULL AND registered_by IS NULL)),
    ADD CONSTRAINT registrations_named_party CHECK (household_person_id IS NULL OR (guest_count = 0 AND childcare_count = 0 AND childcare_ages_note = '')),
    ADD CONSTRAINT registrations_named_care CHECK (NOT childcare_requested OR (household_person_id IS NOT NULL AND attendance_mode = 'in_person'));
-- idx_registration (occurrence_id, user_id) stays a full unique index: NULL
-- user_ids are distinct, and it also serves occurrence_id lookups (Whittler).
CREATE UNIQUE INDEX idx_household_registration ON registrations(occurrence_id, household_person_id) WHERE household_person_id IS NOT NULL;
CREATE INDEX idx_registrations_registered_by ON registrations(registered_by) WHERE registered_by IS NOT NULL;
CREATE INDEX idx_registrations_household_person_id ON registrations(household_person_id) WHERE household_person_id IS NOT NULL;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM registrations WHERE household_person_id IS NOT NULL) THEN
        RAISE EXCEPTION 'household registrations exist; explicitly remove those rows or restore a pre-upgrade backup before rolling back';
    END IF;
END $$;
-- +goose StatementEnd
DROP INDEX idx_household_registration;
DROP INDEX idx_registrations_registered_by;
DROP INDEX idx_registrations_household_person_id;
ALTER TABLE registrations
    DROP CONSTRAINT registrations_subject,
    DROP CONSTRAINT registrations_holder,
    DROP CONSTRAINT registrations_named_party,
    DROP CONSTRAINT registrations_named_care,
    DROP COLUMN household_person_id,
    DROP COLUMN registered_by,
    DROP COLUMN childcare_requested;
ALTER TABLE registrations ALTER COLUMN user_id SET NOT NULL;
