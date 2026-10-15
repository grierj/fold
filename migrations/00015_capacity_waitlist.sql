-- +goose Up
-- M1.5 (5.4): event capacity, waitlist, plus-ones, and the registration
-- lifecycle for waitlist and pruning. Decision 1 (owner, Oct 15): no
-- remote columns here; remote capacity arrives with M1.8.

ALTER TABLE events ADD COLUMN capacity bigint
    CONSTRAINT events_capacity_check CHECK (capacity IS NULL OR capacity > 0);
ALTER TABLE events ADD COLUMN waitlist_enabled boolean NOT NULL DEFAULT false;
ALTER TABLE events ADD COLUMN guests_allowed bigint NOT NULL DEFAULT 0
    CONSTRAINT events_guests_allowed_check CHECK (guests_allowed BETWEEN 0 AND 5);

ALTER TABLE event_occurrences ADD COLUMN capacity_override bigint
    CONSTRAINT event_occurrences_capacity_override_check
    CHECK (capacity_override IS NULL OR capacity_override > 0);

ALTER TABLE registrations ADD COLUMN guest_count bigint NOT NULL DEFAULT 0
    CONSTRAINT registrations_guest_count_check CHECK (guest_count >= 0);
ALTER TABLE registrations ADD COLUMN waitlisted_at timestamptz;
ALTER TABLE registrations ADD COLUMN promoted_at timestamptz;
ALTER TABLE registrations ADD COLUMN removed_by bigint;
ALTER TABLE registrations ADD COLUMN removal_note text NOT NULL DEFAULT '';

ALTER TABLE registrations DROP CONSTRAINT registrations_status_check;
ALTER TABLE registrations ADD CONSTRAINT registrations_status_check
    CHECK (status IN ('going', 'maybe', 'declined', 'cancelled', 'waitlisted', 'removed'));

-- +goose Down
ALTER TABLE registrations DROP CONSTRAINT registrations_status_check;
ALTER TABLE registrations ADD CONSTRAINT registrations_status_check
    CHECK (status IN ('going', 'maybe', 'declined', 'cancelled'));
ALTER TABLE registrations DROP COLUMN removal_note;
ALTER TABLE registrations DROP COLUMN removed_by;
ALTER TABLE registrations DROP COLUMN promoted_at;
ALTER TABLE registrations DROP COLUMN waitlisted_at;
ALTER TABLE registrations DROP COLUMN guest_count;
ALTER TABLE event_occurrences DROP COLUMN capacity_override;
ALTER TABLE events DROP COLUMN guests_allowed;
ALTER TABLE events DROP COLUMN waitlist_enabled;
ALTER TABLE events DROP COLUMN capacity;
