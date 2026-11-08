-- QC-1 (T07B-2-V) one scenario: k reasons that belong to one live account.
-- psql variables:
--   n            filler rows already loaded (qc1/filler.sql), ids 2, 4, ... 2n
--   k            reasons for the target (0 adds nothing)
--   target       the live account's user id
--   direction    'requester' (target requested), 'subject' (target is the
--                subject) or 'both' (alternating)
--   layout       'spread' (ids evenly across the table), 'tail' (after the last
--                filler row, the worst case for a scan from the start) or
--                'head' (before every filler row)
--   counterparts comma list of real user ids, cycled as the other party
--   group_id     a real group id, the request's context
--   reason_len   length of each reason (1..1000)
-- Scenario ids are odd, so they never collide with filler ids and a later
--   DELETE FROM contact_requests WHERE id % 2 = 1
-- removes exactly the scenario rows. Same row shape and trigger handling as
-- qc1/filler.sql.
\set ON_ERROR_STOP on
SET session_replication_role = replica;
INSERT INTO contact_requests (id, requester_id, subject_user_id, context_type, context_id, fields, reason, needs_admin,
                              subject_decision, subject_decided_by, subject_decided_at, admin_decision, status,
                              created_at, expires_at, completed_at, purpose)
SELECT s.id,
       CASE WHEN s.as_requester THEN :target ELSE s.cp END,
       CASE WHEN s.as_requester THEN s.cp ELSE :target END,
       'group', :group_id, '',
       left(repeat('Scenario reason text. ', 60), :reason_len - length(s.j::text)) || s.j::text,
       true, 'denied',
       CASE WHEN s.as_requester THEN s.cp ELSE :target END,
       s.t0 + interval '1 minute', 'pending', 'denied',
       s.t0, s.t0 + interval '14 days', s.t0 + interval '2 minutes', 'group_add'
FROM (
  SELECT j.j,
         CASE :'layout'
           WHEN 'tail' THEN 2::bigint * :n + 2 * j.j + 1
           WHEN 'head' THEN 2 * j.j - 1
           ELSE 2 * ((j.j - 1)::bigint * :n / :k) + 1
         END AS id,
         (:'direction' = 'requester' OR (:'direction' = 'both' AND j.j % 2 = 0)) AS as_requester,
         (string_to_array(:'counterparts', ','))[1 + (j.j % cardinality(string_to_array(:'counterparts', ',')))]::bigint AS cp,
         timestamptz '2026-02-01 00:00:00+00' + (j.j % 100000) * interval '1 minute' AS t0
  FROM generate_series(1, :k) AS j(j)
) AS s;
