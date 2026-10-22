-- +goose Up
-- M1.8 slice 2 (5.3, 5.4): remote attendance. An event may offer remote
-- seats counted against their own capacity, separate from the in-person
-- pool (DEC-38/47 apply per pool). A per-date override mirrors
-- capacity_override. Every registration keeps the mode it signed up in.

ALTER TABLE events ADD COLUMN remote_available boolean NOT NULL DEFAULT false;
ALTER TABLE events ADD COLUMN remote_capacity bigint
    CONSTRAINT events_remote_capacity_check CHECK (remote_capacity IS NULL OR remote_capacity >= 0);

ALTER TABLE event_occurrences ADD COLUMN remote_capacity_override bigint
    CONSTRAINT event_occurrences_remote_capacity_override_check
    CHECK (remote_capacity_override IS NULL OR remote_capacity_override >= 0);

ALTER TABLE registrations ADD COLUMN attendance_mode text NOT NULL DEFAULT 'in_person'
    CONSTRAINT registrations_attendance_mode_check
    CHECK (attendance_mode IN ('in_person', 'remote'));

-- +goose Down
ALTER TABLE registrations DROP COLUMN attendance_mode;
ALTER TABLE event_occurrences DROP COLUMN remote_capacity_override;
ALTER TABLE events DROP COLUMN remote_capacity;
ALTER TABLE events DROP COLUMN remote_available;
