-- +goose Up
-- Room booking approval (M2.1d, DEC-102, DEC-105, DEC-106). With the
-- room_bookings_need_approval setting on, a leader's or host's booking is a
-- request an admin confirms or rejects. One request covers one booking
-- action: a date, or every date of a series. Its dates are requested
-- bookings (holding nothing) that point at it; a requested booking with no
-- pending request is a date that lost its room, the leader's to re-pick.
CREATE TABLE room_requests (
    id bigserial PRIMARY KEY,
    event_id bigint NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    room_id bigint NOT NULL REFERENCES rooms(id),
    -- series: the request is for the series room, so approving it lets the
    -- daily extender book new dates in it, and rejecting it stops it.
    series boolean NOT NULL DEFAULT false,
    requested_by bigint REFERENCES users(id),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'confirmed', 'rejected', 'withdrawn')),
    decided_by bigint REFERENCES users(id),
    decided_at timestamptz,
    reject_reason text NOT NULL DEFAULT '' CHECK (char_length(reject_reason) <= 500),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_room_requests_pending ON room_requests (created_at) WHERE status = 'pending';
CREATE INDEX idx_room_requests_event ON room_requests (event_id);
-- At most one waiting request for a series room per event.
CREATE UNIQUE INDEX idx_room_requests_one_series ON room_requests (event_id) WHERE series AND status = 'pending';

ALTER TABLE room_bookings ADD COLUMN request_id bigint REFERENCES room_requests(id) ON DELETE SET NULL;
CREATE INDEX idx_room_bookings_request ON room_bookings (request_id) WHERE request_id IS NOT NULL;

-- default_room_pending: the series room a leader or host picked has not been
-- approved yet, so every new date of it goes to an admin, even after its
-- request was withdrawn. An admin's confirm clears it; a reject clears the
-- series room.
ALTER TABLE events ADD COLUMN default_room_pending boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE events DROP COLUMN default_room_pending;
DROP INDEX idx_room_bookings_request;
ALTER TABLE room_bookings DROP COLUMN request_id;
DROP TABLE room_requests;
