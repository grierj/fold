-- +goose Up
-- A series' room (M2.1c, 5.3, 9.2). "Book for the whole series" stores the
-- room here and books every scheduled date; dates the daily extender and
-- series edits add later get a booking in it too. Null means no series room:
-- dates may still have their own bookings.
ALTER TABLE events ADD COLUMN default_room_id bigint REFERENCES rooms(id);

-- Admins' double-bookings sit outside the exclusion constraint's index, and
-- every booking and move checks them (heldByOverlap); a whole series checks
-- once per date.
CREATE INDEX idx_room_bookings_overlap ON room_bookings USING gist (room_id, padded_range)
    WHERE allow_overlap AND status = 'confirmed';

-- +goose Down
DROP INDEX idx_room_bookings_overlap;
ALTER TABLE events DROP COLUMN default_room_id;
