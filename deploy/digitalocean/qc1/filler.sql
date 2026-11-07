-- QC-1 (T07B-2-V) background rows: contact_requests that belong to nobody the
-- measurements look at. Run with psql variables (-v) n, reason_len, reasons.
--   n          rows to add (ids 2, 4, ... 2n, so odd ids stay free for scenarios)
--   reason_len length of each reason when it has one (1..1000)
--   reasons    'all' (every row has a reason), 'half' (even i) or 'none'
-- Parties are ids far above any real account (1000000+ and 2000000+), distinct
-- from each other, so the check requester_id <> subject_user_id holds. Rows are
-- denied group_add requests, the shape migrations/reason_review_test.go proves
-- against the schema. The deferred add_request_completed trigger is skipped
-- with session_replication_role = replica, the way internal/testdb loads data
-- (CHECK constraints still apply). Superuser only: a disposable database.
\set ON_ERROR_STOP on
SET session_replication_role = replica;
INSERT INTO contact_requests (id, requester_id, subject_user_id, context_type, context_id, fields, reason, needs_admin,
                              subject_decision, subject_decided_by, subject_decided_at, admin_decision, status,
                              created_at, expires_at, completed_at, purpose)
SELECT 2 * g.i,
       1000000 + (g.i % 5000),
       2000000 + (g.i % 5000),
       'group', 1, '',
       CASE WHEN :'reasons' = 'all' OR (:'reasons' = 'half' AND g.i % 2 = 0)
            THEN left(repeat('Filler reason text. ', 60), :reason_len - length(g.i::text)) || g.i::text
            ELSE '' END,
       true, 'denied', 2000000 + (g.i % 5000), g.t0 + interval '1 minute', 'pending', 'denied',
       g.t0, g.t0 + interval '14 days', g.t0 + interval '2 minutes', 'group_add'
FROM (SELECT i, timestamptz '2026-01-01 00:00:00+00' + (i % 100000) * interval '1 minute' AS t0
      FROM generate_series(1, :n) AS i) AS g;
