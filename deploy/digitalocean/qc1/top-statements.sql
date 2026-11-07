-- QC-1: the statements that cost the most database time in the window just measured, as one JSON array (printed with psql -At).
-- Read AFTER the window closes and BEFORE the next pg_stat_statements_reset(), so it adds nothing to the operation's own timing and does not reset anything:
-- the COMMIT timing (qc1_commit) reads the same counters and is untouched. The counters are cluster-wide, from the reset at the start of the window until this
-- query runs (a few hundred milliseconds after the window ends): they include the scheduler, any worker and anything else the database served, so a statement
-- here is NOT attributed to the operation. The harness's own sampler, bookkeeping and transaction-control statements are filtered out by their text (the filter
-- is a text match, not a proof). pg_stat_statements itself costs a little on every statement; it is on for the COMMIT figures already.
SELECT coalesce(json_agg(t), '[]'::json)::text
FROM (
  SELECT left(regexp_replace(query, '\s+', ' ', 'g'), 200) AS query,
         calls,
         round(total_exec_time::numeric, 1) AS "totalMs",
         round(mean_exec_time::numeric, 2) AS "meanMs",
         rows,
         shared_blks_hit AS "sharedHit",
         shared_blks_read AS "sharedRead",
         wal_bytes AS "walBytes"
  FROM pg_stat_statements
  WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
    AND query !~* '(pg_stat_statements|pg_locks|pg_current_wal_lsn|pg_stat_activity|pg_stat_database|pg_stat_bgwriter|pg_stat_wal|pg_stat_user_tables|pg_advisory|pg_sleep|pg_wal_lsn_diff|checkpoint)'
    AND query !~* '^ *(begin|rollback|set |show |deallocate|savepoint|release|discard)'
  ORDER BY total_exec_time DESC
  LIMIT 8
) t;
