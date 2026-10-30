-- +goose Up
-- Rooms (M2.1a, 5.3, 9.1): the catalog admins manage. Optional by design:
-- nothing about rooms shows until the rooms_enabled setting is on. Rooms are
-- never deleted, only deactivated, so bookings keep their history.
-- default_setup_minutes and default_teardown_minutes are copied into each
-- booking's padding (DEC-101).
CREATE TABLE rooms (
    id bigserial PRIMARY KEY,
    name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 100),
    building text NOT NULL DEFAULT '' CHECK (length(building) <= 100),
    capacity bigint CHECK (capacity BETWEEN 1 AND 100000),
    features text NOT NULL DEFAULT '' CHECK (length(features) <= 500),
    notes text NOT NULL DEFAULT '' CHECK (length(notes) <= 2000),
    active boolean NOT NULL DEFAULT true,
    restricted boolean NOT NULL DEFAULT false,
    default_setup_minutes bigint NOT NULL DEFAULT 0 CHECK (default_setup_minutes BETWEEN 0 AND 240),
    default_teardown_minutes bigint NOT NULL DEFAULT 0 CHECK (default_teardown_minutes BETWEEN 0 AND 240),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
-- One "Fellowship Hall" per building, whatever the letter case.
CREATE UNIQUE INDEX idx_rooms_building_name ON rooms (lower(building), lower(name));

-- +goose Down
DROP TABLE rooms;
