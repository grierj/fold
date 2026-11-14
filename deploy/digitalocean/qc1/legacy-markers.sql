-- QC-1 (T-292) forged decision markers. READ THIS BEFORE YOU RUN IT.
--
-- This file writes audit rows that say an admin already decided a deleted
-- account's reasons. That hides those reasons from the deleted-accounts review
-- list. On a real church database that would silently drop undecided reasons
-- from the review. It exists only so the QC-1 harness can measure the marker
-- lookup against a table of 100% decided pairs without sending 1,000,000 saves.
--
-- It runs only through qc1/legacy.sh (forge_markers): psql into the harness's
-- own disposable database container, by id. The guard below refuses anywhere
-- else. The guard does not trust anything a real database could also hold (an
-- email domain, a site name alone): it needs a row the harness wrote into its
-- own disposable database (table qc1_disposable) whose token is the one this
-- run passed in, whose system identifier is this cluster's, and whose site name
-- is still QC-1. A refusal raises inside the transaction: nothing is inserted
-- and psql exits non-zero.
--
-- psql variables (all required, none defaulted):
--   token      the run's disposable-database token (qc1_disposable.token)
--   tombstone  the soft-deleted account whose pairs are marked
--   only_id    one request id to mark, or 0 for every reason-bearing request of the tombstone
-- Only group_add rows are marked: the harness keeps no second copy of the
-- application's request-kind rule (request_kinds.go), so a request of any other
-- purpose is refused rather than guessed.
\set ON_ERROR_STOP on
BEGIN;
SELECT set_config('qc1.token', :'token', true), set_config('qc1.tombstone', :'tombstone', true), set_config('qc1.only_id', :'only_id', true);
DO $guard$
DECLARE
  t bigint := current_setting('qc1.tombstone')::bigint;
  o bigint := current_setting('qc1.only_id')::bigint;
BEGIN
  IF to_regclass('public.qc1_disposable') IS NULL THEN
    RAISE EXCEPTION 'qc1 legacy-markers: this is not the QC-1 disposable database (no qc1_disposable table)';
  END IF;
  IF (SELECT count(*) FROM public.qc1_disposable) <> 1
     OR NOT EXISTS (SELECT 1 FROM public.qc1_disposable d
                    WHERE d.token = current_setting('qc1.token') AND d.system_identifier = (SELECT system_identifier FROM pg_control_system())) THEN
    RAISE EXCEPTION 'qc1 legacy-markers: the disposable-database token or cluster does not match this run';
  END IF;
  IF (SELECT value FROM settings WHERE key = 'site_name') IS DISTINCT FROM 'QC-1' THEN
    RAISE EXCEPTION 'qc1 legacy-markers: the site is not QC-1';
  END IF;
  IF t <= 0 OR o < 0 THEN
    RAISE EXCEPTION 'qc1 legacy-markers: tombstone must be positive and only_id zero or positive';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM users WHERE id = t AND deleted_at IS NOT NULL AND email = 'deleted-' || t || '@deleted.invalid') THEN
    RAISE EXCEPTION 'qc1 legacy-markers: the target is not a soft-deleted account with the tombstone email';
  END IF;
  IF EXISTS (SELECT 1 FROM contact_requests WHERE (requester_id = t OR subject_user_id = t) AND (o = 0 OR id = o) AND reason <> '' AND purpose <> 'group_add') THEN
    RAISE EXCEPTION 'qc1 legacy-markers: only group_add requests are marked';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM contact_requests WHERE (requester_id = t OR subject_user_id = t) AND (o = 0 OR id = o) AND reason <> '') THEN
    RAISE EXCEPTION 'qc1 legacy-markers: there is nothing to mark';
  END IF;
END
$guard$;
-- The shape of a marker the application writes (eraseContactRequestReasonsIn, audit.WriteAll): the add_request target,
-- the kept action, erasedUserId and reviewVersion as JSON numbers, and the same seven meta keys. The actor is the QC admin.
INSERT INTO audit_logs (actor_id, action, target_type, target_id, meta_json, ip)
SELECT (SELECT id FROM users WHERE email = 'admin@example.invalid' AND deleted_at IS NULL), 'add.reason_kept', 'add_request', cr.id,
       jsonb_build_object('erasedUserId', p.t, 'erasedRole', CASE WHEN cr.requester_id = p.t THEN 'requester' ELSE 'subject' END,
                          'purpose', cr.purpose, 'status', cr.status, 'reviewerIsParty', false, 'source', 'legacy_review', 'reviewVersion', 1), ''
FROM contact_requests cr,
     (SELECT current_setting('qc1.tombstone')::bigint AS t, current_setting('qc1.only_id')::bigint AS o) p
WHERE (cr.requester_id = p.t OR cr.subject_user_id = p.t) AND (p.o = 0 OR cr.id = p.o) AND cr.reason <> '';
COMMIT;
