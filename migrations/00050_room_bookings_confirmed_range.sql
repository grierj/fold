-- +goose Up
-- Room suggestions (M2.3a, 9.3 Rule 2) and the room pickers ask which rooms
-- a confirmed booking holds over some times. The exclusion constraint's
-- index leaves out admins' double-bookings (allow_overlap), so this one
-- covers every confirmed booking.
CREATE INDEX idx_room_bookings_confirmed_range ON room_bookings USING gist (room_id, padded_range) WHERE status = 'confirmed';

-- +goose Down
DROP INDEX idx_room_bookings_confirmed_range;
