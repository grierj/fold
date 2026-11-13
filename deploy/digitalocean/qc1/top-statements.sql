-- QC-1: the statements that cost the most database time in the window just measured, as one JSON array (printed with psql -At).
-- Read AFTER the window closes and BEFORE the next pg_stat_statements_reset(), so it adds nothing to the operation's own timing and does not reset anything:
-- the COMMIT timing (qc1_commit) reads the same counters and is untouched. The counters are cluster-wide, from the reset at the start of the window until this
-- query runs (a few hundred milliseconds after the window ends): they include the scheduler, any worker and anything else the database served, so a statement
-- here is NOT attributed to the operation. The harness's own sampler, bookkeeping (audit rows and bytes, audit rows by action, the row count, the scheduler-row count) and transaction-control statements are filtered out by their text (the filter
-- is a text match, not a proof). pg_stat_statements itself costs a little on every statement; it is on for the COMMIT figures already.
-- :lim is the number of statements kept (psql -v lim=; 8 unless a diagnostic asks for more). blkReadMs and blkWriteMs are track_io_timing, which the harness turns on: the time a statement waited on block I/O.
-- isAudit is decided from the full query text before the 200-character display cut, so a long statement that names audit_logs late still counts.
SELECT coalesce(json_agg(t), '[]'::json)::text
FROM (
  SELECT left(regexp_replace(query, '\s+', ' ', 'g'), 200) AS query,
         (query ~* 'audit_logs') AS "isAudit",
         calls,
         round(total_exec_time::numeric, 1) AS "totalMs",
         round(mean_exec_time::numeric, 2) AS "meanMs",
         rows,
         shared_blks_hit AS "sharedHit",
         shared_blks_read AS "sharedRead",
         wal_bytes AS "walBytes",
         round(blk_read_time::numeric, 1) AS "blkReadMs",
         round(blk_write_time::numeric, 1) AS "blkWriteMs"
  FROM pg_stat_statements
  WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
    AND query !~* '(pg_stat_statements|pg_locks|pg_current_wal_lsn|pg_stat_activity|pg_stat_database|pg_stat_bgwriter|pg_stat_wal|pg_stat_user_tables|pg_advisory|pg_sleep|pg_wal_lsn_diff|checkpoint|pg_total_relation_size|jsonb_object_agg\(action|from jobs where created_at >= to_timestamp)'
    AND query !~* '^ *select count\(\*\) from contact_requests *$'
    AND query !~* '^ *(begin|rollback|set |show |deallocate|savepoint|release|discard)'
  ORDER BY total_exec_time DESC
  LIMIT :lim
) t;
