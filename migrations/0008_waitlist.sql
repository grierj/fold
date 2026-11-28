-- Slice 2, milestone 2b: seats and the waitlist.

-- A place on the waitlist counts like an answer: a disabled account's is
-- hidden and keeps no place (DEC-38). The line is in waiting_since order.
DROP VIEW counted_answers;
CREATE VIEW counted_answers AS
  SELECT a.id, a.date_id, a.person_id, a.status, a.waiting_since
  FROM answers a JOIN people p ON p.id = a.person_id
  WHERE a.status IN ('going', 'maybe', 'cant', 'waiting') AND p.status = 'active';
