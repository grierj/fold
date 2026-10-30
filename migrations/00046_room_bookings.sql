-- +goose Up
-- Room bookings (M2.1b, 5.3, 9.2). A booking belongs to one occurrence and
-- holds its room for the occurrence's time plus the room's setup and
-- teardown padding (DEC-101), stored as padded_range. The exclusion
-- constraint lets no two confirmed bookings of one room overlap, so two
-- leaders booking at the same moment can't both win; an admin's audited
-- double-booking (allow_overlap, with a reason) is outside it. A requested
-- or rejected booking holds nothing.
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE room_bookings (
    id bigserial PRIMARY KEY,
    occurrence_id bigint NOT NULL REFERENCES event_occurrences(id) ON DELETE CASCADE,
    room_id bigint NOT NULL REFERENCES rooms(id),
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL,
    setup_minutes bigint NOT NULL DEFAULT 0 CHECK (setup_minutes BETWEEN 0 AND 240),
    teardown_minutes bigint NOT NULL DEFAULT 0 CHECK (teardown_minutes BETWEEN 0 AND 240),
    padded_range tstzrange NOT NULL,
    status text NOT NULL CHECK (status IN ('confirmed', 'requested', 'rejected')),
    allow_overlap boolean NOT NULL DEFAULT false,
    overlap_reason text NOT NULL DEFAULT '' CHECK (length(overlap_reason) <= 500),
    created_by bigint REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT room_bookings_times CHECK (ends_at > starts_at),
    -- The app writes padded_range from the same values; a timestamptz minus
    -- an interval isn't immutable, so it can't be a generated column.
    CONSTRAINT room_bookings_padded_range CHECK (padded_range = tstzrange(
        starts_at - make_interval(mins => setup_minutes::int),
        ends_at + make_interval(mins => teardown_minutes::int), '[)')),
    CONSTRAINT room_bookings_overlap_reason CHECK (NOT allow_overlap OR length(btrim(overlap_reason)) > 0),
    CONSTRAINT room_bookings_no_overlap EXCLUDE USING gist (room_id WITH =, padded_range WITH &&)
        WHERE (status = 'confirmed' AND NOT allow_overlap)
);
-- One room per date.
CREATE UNIQUE INDEX idx_room_bookings_occurrence ON room_bookings (occurrence_id);
CREATE INDEX idx_room_bookings_room ON room_bookings (room_id, starts_at);

-- +goose Down
DROP TABLE room_bookings;
