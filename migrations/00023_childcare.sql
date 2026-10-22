-- +goose Up
ALTER TABLE events ADD COLUMN childcare_available boolean NOT NULL DEFAULT false;
ALTER TABLE events ADD COLUMN childcare_notes text NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN childcare_capacity bigint
    CONSTRAINT events_childcare_capacity_check CHECK (childcare_capacity >= 0);
ALTER TABLE events ADD COLUMN childcare_ages text NOT NULL DEFAULT '';
ALTER TABLE registrations ADD COLUMN childcare_count bigint NOT NULL DEFAULT 0
    CONSTRAINT registrations_childcare_count_check CHECK (childcare_count >= 0);
ALTER TABLE registrations ADD COLUMN childcare_ages_note text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE registrations DROP COLUMN childcare_ages_note;
ALTER TABLE registrations DROP COLUMN childcare_count;
ALTER TABLE events DROP COLUMN childcare_ages;
ALTER TABLE events DROP COLUMN childcare_capacity;
ALTER TABLE events DROP COLUMN childcare_notes;
ALTER TABLE events DROP COLUMN childcare_available;
