-- Slice 2, milestone 3a: an admin may turn a guest off (R48). Turning the
-- account on again gives back what it was, a member or a guest, so the
-- status it was turned off from is kept while it is off.
ALTER TABLE people ADD COLUMN disabled_from TEXT CHECK (disabled_from IN ('active', 'guest'));
