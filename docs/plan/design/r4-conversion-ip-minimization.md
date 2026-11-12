# Design R4-4b: bounded conversion-IP minimization (option P2)

**Design revision r3.** Design only. This file changes no production code, migration, sweep
version, test, audit row or owner decision. It is the input to the independent review that
issue #303 requires (DG-9 item 3), and to the implementation in #305. An independent
reviewer approved r3 at `ead7e7bc9e1b135ceabc6fd34adfaebabde6bc81` (section 12); the
subsequent record and status corrections change no design. #305 stays deferred until the
#300 verification it depends on is satisfied. Revision r1 received combined independent
findings R303-01, R303-02, F-COUNT, F-GAP, F-RESIDUE and F-LOCK. A review of r2 left
R303-02, F-COUNT and F-RESIDUE open; the r3 review closed them.

Sources: `docs/plan/M7.4-ERASURE-INVENTORY.md` (sections 1, 3, 6.5.1, 6.5.2, 6.9, 12.1),
`docs/agents/erasure.md`, `account_erasure_audit.go`, `account_erasure_tombstones.go`,
`api_teen_conversion.go`, `api_admin.go`, `api_admin_guardian_links.go`, `api_teen_consent.go`,
and the R4-GR-IP review (`gr-ip.json`, part 2, items 11 and 12). Names below are the
identifiers at the base this design was written against; the code is the truth if they drift.

## 1. Authority and scope

Owner decision 5 (Nov 3, 2026; question and answer verbatim in inventory section 1) answered
**"Yes, clear those IPs (Recommended)"**. It authorizes the *concept* of option P2 in exactly
this scope, and nothing wider:

- the entries are the identified household-conversion audit rows: `household.person_converted`
  and its conversion-shaped `registration.changed` rows, and no other action;
- when the erased person **may have caused** the conversion;
- clear **only the `ip` column**, even if the address may belong to another living participant;
- never where the address is essential safeguarding evidence; decisions, actors, dates and
  provenance remain.

R4-GR-IP part 2 judged the `ip` of both actions **not essential** (items 11 and 12), and
created no RETAIN_ESSENTIAL action and no Q4 exception. It "approves no selector". It also
judged `guardian_link.requested` with `source = household_conversion` not essential, but that
action is **not** one of decision 5's two actions, and this design does not touch it (item 13:
"R4-4b must not use that decision to widen its scrub").

This design chooses nothing beyond P2. It does not propose, authorize or prepare P1, P3, P4 or
P5. It does not establish G-8 (history precondition: stays **unestablished**) and does not
close G-9 (stays **OPEN**). It does not decide whether a group retained as malformed or
ambiguous counts as "identified" for the owner, nor whether a request with a missing
identity link does; it takes the conservative reading (such groups are never touched,
section 5, G1 to G5) and says so as judgment calls for the reviewer (section 13). Q3 —
the owner's disposition of every residue item in section 11 — stays open and is not
answered here.

Out of scope here, by the task: changes to the R4-4a attribution rules, the other audit
actions, any step row of a living person, `account_requests.ip`, migrations, the delete dialog
copy (#308), the owner's disposition of residue (#304), and the implementation (#305).

## 2. Terms

`U` is the erased user id. For an approved conversion request `R` (`account_requests` row with
`source = 'household_conversion'`, `status = 'approved'`): `P = R.household_person_id`,
`I = R.invitation_id`, `S = R.approved_user_id` (the converted account), `D = R.decided_by`,
`H` = the household of `P`, read unscoped from `household_people`. These are the inventory's
names (6.5.1). `C` is the one `household.person_converted` row of a conversion and `T` its
`registration.changed` transfer rows. The *group* of `R` is the discovered set `CC(R) ∪ CT(R)`
of 6.5.1, produced today by `discoverAuditConversionsIn`.

## 3. The candidate-caller set

`U` is a *candidate caller* of `R` when **any one** of five retained keys names `U`. The set is
exactly the one issue #303 lists. No selector reads a time, an adjacency, an address value, a
name, `household_people.kind` or `users.account_type` (inventory rules in 3 and 6.5.1).

| # | Selector (retained key) | Who it names | Why it is a possible caller |
|---|---|---|---|
| K1 | `account_requests.decided_by = U` | the approving admin `D` | caused the approval transfer, and may have completed it |
| K2 | `invitations.invited_by = U` for `invitations.id = R.invitation_id` | the inviter | the conversion's `actor_id`; may also be an admin who completed it |
| K3 | `guardian_links.confirmed_by = U` for a link with `teen_user_id = S` | the admin who confirmed a teen-side link | a link confirmation calls completion (`api_admin_guardian_links.go`) |
| K4 | `teen_consents.granted_by = U` for `teen_user_id = S`, `scope = 'account'`, `status = 'granted'` | the admin or confirmed guardian who recorded account consent | a consent record calls completion (`api_teen_consent.go`) |
| K5 | `audit_logs` row `action = 'user.enabled'`, `target_type = 'user'`, `target_id = S`, `actor_id = U` | an admin who enabled `S` | an Enable calls completion (`api_admin.go`) |

Two deliberate readings, flagged in section 13:

- **K4 is narrower than "`teen_consents.granted_by`".** Only an account-scope, granted row can
  make a conversion ready; a revoked row or a `photos`/`sms` row cannot complete anything, so
  its granter is not a *possible* caller. Narrowing stays inside the owner's scope (it clears
  less) and is the easy direction to widen if the reviewer disagrees.
- **No time key, so the set is over-inclusive on purpose.** A guardian who confirmed a second
  link *after* completion, or an admin who enabled `S` long after, is in the set. "May have
  caused" is a statement about possibility; the owner accepted that a living participant's
  duplicate address can be cleared. Tests pin this as expected behavior (section 10, P2-9).

**The selector.** One query returns the candidate requests of `U`, with the columns
`discoverAuditConversionsIn` already reads, so that one candidate-row query still serves all
of the erased person's conversions (inventory 6.5.1 "one pass, not one scan per request"):

An approved conversion request stays discoverable for diagnostics even when one of its
identity links is missing. The request query therefore has **no** `approved_user_id IS NOT
NULL` filter (r1 had one; R303-01): `approved_user_id` is nullable in the schema
(`migrations/00001_baseline.sql:407`) and its foreign key is `ON DELETE SET NULL`
(`:3275`), while the existing discovery reads every approved request with
`decided_by = U` (`account_erasure_audit.go:366-369`). A NULL link also escapes the
partial unique indexes on approved conversions (baseline 2588, 2595), so the
"at most one `R` per `P` and per `S`" claim does not hold for link-missing requests.
Missing links never authorize clearing: section 5 admits a group to the cleared set only
when `R`'s `household_person_id`, `invitation_id` and `approved_user_id` are all
non-NULL. Reporting eligibility (counted, retained) and clearing eligibility (changed)
are different gates.

```sql
SELECT r.id, r.household_person_id AS person_id, r.invitation_id, r.approved_user_id AS user_id, h.household_id,
 EXISTS (SELECT 1 FROM audit_logs m WHERE m.action = 'household.person_updated' AND m.target_type = 'household_person' AND m.target_id = r.household_person_id) AS marker
FROM account_requests r LEFT JOIN household_people h ON h.id = r.household_person_id
WHERE r.source = 'household_conversion' AND r.status = 'approved'
 AND (r.decided_by = CAST(@uid AS bigint)
  OR EXISTS (SELECT 1 FROM invitations i WHERE i.id = r.invitation_id AND i.invited_by = CAST(@uid AS bigint))
  OR EXISTS (SELECT 1 FROM guardian_links g WHERE g.teen_user_id = r.approved_user_id AND g.confirmed_by = CAST(@uid AS bigint))
  OR EXISTS (SELECT 1 FROM teen_consents c WHERE c.teen_user_id = r.approved_user_id AND c.granted_by = CAST(@uid AS bigint)
             AND c.scope = 'account' AND c.status = 'granted')
  OR EXISTS (SELECT 1 FROM audit_logs e WHERE e.action = 'user.enabled' AND e.target_type = 'user'
             AND e.target_id = r.approved_user_id AND e.actor_id = CAST(@uid AS bigint)))
```

All five keys survive erasure: users are only soft-deleted, and no erasure step writes
`decided_by`, `invited_by`, `confirmed_by`, `granted_by` or an audit `actor_id` (T01, T12, T36,
T17 and the audit rows; `eraseHouseholdRecordsIn` revokes links but keeps `confirmed_by`), so the same query gives the same answer for a fresh deletion and for a
tombstone swept later. For requests whose links are all present, the two partial unique
indexes on approved conversions (baseline 2588, 2595) allow at most one `R` per `P` and
per `S`. Requests with a NULL link are outside those indexes; they are still discovered
and reported, but section 5 never admits them to the cleared set. The K3 to K5 arms
compare against `r.approved_user_id`, so a NULL `approved_user_id` simply matches no arm;
K1 and K2 still discover the request through `decided_by` and the invitation's
`invited_by` for diagnostics.

Index use (to be confirmed by `EXPLAIN` in #305, which proposes no index): the outer scan reads
approved conversion requests, a population bounded by the number of conversions; K2 uses
`idx_invitations_invited_by`, K3 `idx_guardian_links_teen_user_id`, K4 `idx_teen_consents_latest`
(`teen_user_id` leads), K5 `idx_audit_logs_target`. A `meta_json` index or any new index is a
schema change for the database/performance review, not part of this design.

## 4. Placement and order inside `eraseAuditDetailsIn`

`eraseAuditDetailsIn` stays where it is in `eraseAccountIn` (after dependent records, before
the owned throttle keys, which stay last, D2). Inside it, the order today is: (1) the six
attribution `UPDATE`s by class, (2) the metadata key subtractions, (3) the retention report
`SELECT`, (4) `discoverAuditConversionsIn`. P2 is a **step 5, after (4)**:

1. Steps 1 to 4 are unchanged, except that (4) reads the request set of section 3 instead of
   `decided_by = U` alone and returns, besides its counts, each group's request, its
   verdict and its audit ids (today they are local to the function).
2. Step 5 takes the ids of the *cleared set* (section 5) and runs the one `UPDATE` of section 6.

Why after discovery: validation (V0 to V2) must judge every discovered row before any row is
changed, and 6.5.1 requires the update to run "by the list of `audit_logs.id` just verified,
never by a second query that could drift". Why after steps 1 to 3: the retention report counts
RETAIN_* rows and reads no conversion row (they are class CONVERSION, not RETAIN_*), so P2
cannot change it, and the ACTOR/C_APPROVAL `UPDATE`s touch no conversion row either; the
statements are disjoint by action. `auditRetentionReportSQL` is not edited, which keeps
`deploy/digitalocean/qc1/app-facts.json` (`TestQC1AppFacts`) unchanged.

Transaction and locks: the same transaction as every other erasure step; no new lock, no new
job. Concurrent erasures of two candidates of one conversion are serialized by the exclusive
contact gate both entry points already hold: `apiAdminDeleteUser` runs through
`fencedContactMutation` (`api_admin.go:604`) and `eraseTombstoneOnce` through
`contactMutationCtx` (`account_erasure_tombstones.go:102`), which takes the exclusive
`lockContacts` (`pg_advisory_xact_lock`, `contact_lifecycle.go:30-31,292`). Step 5 adds no
new lock, and no non-erasure writer updates these `audit_logs.ip` values. The cleared id
list is sorted ascending for determinism only: `UPDATE ... WHERE id = ANY(sorted array)`
does not guarantee rows are visited or locked in array order, and this design makes no
lock-order claim from it (F-LOCK). Deadlock freedom beyond the gate is not claimed
(inventory 9.5 already records the erasure as not proven deadlock-free). A failure
anywhere after step 5 rolls its blanks back with the rest of the attempt, and counts of a
rolled-back attempt are never returned (inventory 6.9).

## 5. Which discovered groups may be cleared

Discovery stays as written in 6.5.1 (broad on purpose) and validation stays fail-closed. P2
clears a group only when it is **identified**, defined as all of:

- **G1 (V0)** every discovered row meets its shape (`ShapeC`/`ShapeT`);
- **G2 (V1)** exactly one `household.person_converted` row;
- **G3 (V2)** `transferCount` is a non-negative JSON integer equal to the number of transfer
  rows, and their `target_id` are pairwise distinct;
- **G4** `H` was read (`HouseholdID != nil`, so `HouseholdFallbackSkipped = 0`), so the
  household check of `ShapeC` was real and not skipped;
- **G5** `R`'s `household_person_id`, `invitation_id` and `approved_user_id` are all non-NULL,
  so the candidate set, the join tuple `(P, I, S)` and the K3 to K5 arms all name real rows
  (R303-01). A group whose request lacks any link is retained whole, valid rows included,
  classified `malformed_candidate`, and counted as today.

V3 (history precondition, G-8) and V4 (adult/teen/completion class) are **not read**. P2 does
not classify a row as approval or completion and does not decide who the caller was; that is
what lets it work with G-8 unestablished. The classification reason codes the report already
gives (`history_unestablished` and the failures) are unchanged **as diagnostics**; section 7
defines separately what counts as retained. In code, `classifyAuditConversion`
sets one new field on `auditConversionGroupCounts`, `Identified bool`, to
`G1 && G2 && G3 && G4 && G5`; it is not derived from `Reason`, so a later V4 cannot change it.

Groups that are not identified (`malformed_candidate`, `no_converted_row`,
`duplicate_converted_row`, `count_mismatch`, household unknown) are retained whole, valid
rows included, and counted as today. That is IR-3, and P2 never reaches it.

**The cleared set** is a set of audit ids:

```text
cleared = (union of ids of identified groups) minus (union of ids of every other group)
```

The subtraction costs nothing today (a valid row of one fully-linked request cannot also
match another request's arms, because `P`, `S` and `I` are unique per request; link-missing
requests are outside that claim, which is why G5 excludes them from clearing) and keeps the
rule fail-closed
if a restored backup or a manual repair breaks that. Total per group is the actual discovered
number, never `1 + transferCount` assumed (6.5.1).

## 6. The clearing statement

One statement per chunk of the cleared set (`eachIDChunk`, `lookupChunk` = 5000 ids; ids
sorted ascending for determinism only, not for lock order — section 4; the array bound with
`pgBigintArray`, as the other statements in the file):

```sql
UPDATE audit_logs SET ip = ''
WHERE id = ANY(CAST(@ids AS bigint[])) AND ip <> ''
  AND ((action = 'household.person_converted' AND target_type = 'household')
    OR (action = 'registration.changed' AND target_type = 'registration'
        AND (jsonb_exists(meta_json, 'invitationId') OR jsonb_exists(meta_json, 'formerRegisteredBy'))))
RETURNING id
```

The action/target/key predicate repeats what validation already proved, so a wrong id list can
never change a different kind of row. Everything else about the statement is a constraint:

- **Changed:** `ip` of the listed rows, from a non-empty value to `''` (the writer's own
  "no address" value, `internal/audit/audit.go`). A `NULL` or already-empty `ip` is not
  rewritten (`ip <> ''` is false), so a rerun changes no row. `RETURNING id` returns the
  rows actually changed, which section 7 maps back to groups for per-group `Blanked`.
- **Never changed:** `action`, `target_type`, `target_id`, `actor_id`, `at`, `meta_json` (so
  `personId`, `userId`, `invitationId`, `householdId`, `accountType`, `transferCount`,
  `formerRegisteredBy`, `clearedChildcareRequested`), the three authorization snapshots, the
  age-rollover marker, and every column of `account_requests`, `guardian_links`,
  `teen_consents`, `invitations` and `household_people`.
- **Never selected by:** an address value, a name, a time, `actor_id`, a neighbouring row, or
  a living person's step row. The only inputs are the cleared id set and the two fixed
  action/target pairs.
- **Not touched at all:** the step rows of living people and of `U` alike by this step:
  `user.enabled`, `guardian_link.confirmed`/`.rejected`, `teen_consent.granted`, `account.approved`,
  and `guardian_link.requested` with `source = household_conversion`. `U`'s own step rows are
  blanked, as before, only by the existing ACTOR/C_APPROVAL rules in step 1; a living
  candidate's step rows keep their address.

## 7. Counts and report semantics

`Conversion.Blanked` is the number of rows the step-5 statement changed in this run (the sum
of `RowsAffected`); `BlankedByClass[auditIPConversion]` and `IPsBlanked` take the same number,
so the existing totals stay consistent. New meaning of `Conversion.Retained`: **distinct
discovered rows that are not in the cleared set**, whatever their current `ip`. Therefore
`Retained + |cleared| = Discovered`. An identified group contributes zero retained rows only
when none of its ids is excluded by the overlap subtraction, even when addresses were already
empty (e.g. an age-job completion). A rerun
reports `Blanked = 0` with the same `Retained`. `activityEntriesKept` (the sum of RETAIN_* counts
and `Conversion.Retained`, `newErasureReport`) falls by `|cleared|` relative to today's
`Retained = Discovered`; it still describes
entries, never personal addresses, and still claims nothing about completeness. The response
and the `user.erasure_swept` marker keep their fields; no new key is added to either. The
delete dialog's statement that some addresses can remain is unchanged (G-9 is open; copy is
#308's).

All counts above are over **distinct audit row ids**. Per-group detail counts keep the
existing overlap rule (a row discovered under two requests counts in each group's detail,
once in every total); totals count each actual row once
(`account_erasure_audit.go:406-412`). `Groups` is the number of candidate requests in the
(wider) section-3 set. `Discovered` is the number of distinct discovered row ids.
`cleared` is the section-5 id set.

- **Per-group `Details[i]`**: `ConvertedRetained`/`TransfersRetained` = that group's found
  rows minus the group's rows that are in `cleared`. An identified group reports `0`/`0`
  only when all its ids survive overlap subtraction; a non-identified group reports its
  found counts, overlap included, as today. Per-group
  `Blanked` = the number of that group's rows actually changed, attributed from the
  statement's per-row result (`UPDATE ... RETURNING id`, mapped back to groups; when one
  chunked `UPDATE` spans groups, the `RETURNING` ids allocate the count, never an
  estimate). An id excluded from `cleared` by the overlap subtraction contributes `0` to
  every group's `Blanked`. The sum of per-group `Blanked` equals total `Blanked`, and the
  sum of per-group retained counts equals total `Retained` plus the overlap double-count,
  exactly as today's detail-vs-total rule.
- **`ByReason`** stays a diagnostic classification, not a retention ledger: it maps each
  existing reason code to the distinct discovered ids carrying it, computed exactly as
  today (`account_erasure_audit.go:393-412`), including `history_unestablished` for cleared
  identified groups. Clearing does not rewrite a group's reason. A reader must not sum
  `ByReason` as retained rows once clearing exists; `Retained` is the retention count.
  This reconciles P2-4 with N-10: P2-4's "no reason reported as a retention" means no
  reason code feeds a retention total, while the diagnostic classification is unchanged.
- **`Blanked` (total)** = rows actually changed (sum of `RowsAffected`), so `Blanked <=
  |cleared|`, with strict inequality when some cleared rows already had an empty `ip`.
- **Rerun**: discovery is id-only and reads no `ip`, and every selector key survives
  erasure, so a second run for the same subject
  discovers the same groups and the same `cleared` set; the statement matches no row
  (`ip <> ''` is false everywhere in it), so the rerun reports `Blanked = 0` with
  zero for total `Blanked`, every `Details[i].Blanked`, and the conversion contributions
  to `IPsBlanked` and `BlankedByClass`; `Retained`, `Discovered`, `ByReason` and the
  other detail fields are unchanged.

**Worked example.** L1 adult approval with `C` + 2 `T` rows, all holding an address, erased
by `D`: `Groups = 1`, `Discovered = 3`, `cleared` = all 3 ids, `Retained = 0`,
`Blanked = 3`, `Details[0] = {ConvertedRetained: 0, TransfersRetained: 0, Blanked: 3}`,
`ByReason = {history_unestablished: 3}` (diagnostic only). Rerun: total and detail `Blanked`
and the conversion contributions to `IPsBlanked` and `BlankedByClass` are zero; other
fields are identical. L3 age-job completion (addresses already `""`): first run `Discovered = 3`,
`Retained = 0`, `Blanked = 0`, `ByReason = {history_unestablished: 3}`. With two
conversions where `U` is a candidate of `R1` only: `R1`'s group contributes its cleared
ids, `R2`'s valid group is retained whole with its found counts, and totals follow the
distinct-id arithmetic above.

## 8. The tombstone sweep, version 2

`tombstoneSweepVersion` becomes `2` in #305 (and only there). Mechanics, all existing:

- `tombstoneCandidatesSQL` (`NOT tombstoneSweptSQL`) returns every soft-deleted
  `deleted-<id>@deleted.invalid` account without a `user.erasure_swept` marker whose numeric
  `version` is at least 2. Every tombstone with a version-1 marker, and any with none, is
  therefore discovered once. The version is in the job unique keys
  (`erasure.tombstone:<id>:v2:<day>`, page `v2`), so no version-1 key collides.
- `eraseTombstoneOnce(ctx, id, 2)` runs the whole `eraseAccountIn` pipeline with no held
  address and `erasureOptions{}`. Steps 1 to 4 of `eraseAuditDetailsIn` and every earlier step
  are already idempotent. Step 5 is new work only for a tombstone whose conversion groups were
  not cleared when it was erased (any tombstone erased before P2 shipped, or swept before).
  Its selectors are id-only, so the missing address changes nothing.
- The version-2 transition adds, per tombstone: **one** `user.erasure_swept` marker with
  `version = 2` (the version-1 marker stays; audit rows are never deleted and the older marker
  is covered by the newer, `tombstoneSweptSQL`), **at most one** queued `avatar.cleanup` (L4,
  coalesced), and the P2 blanks listed in `Blanked`. A tombstone with no P2 rows gets only the
  first two. A second version-2 run, and the second daily sweep, enqueue no subject job and change
  no table: the marker satisfies `tombstoneSweptSQL`, and even without it step 5 matches no row.
- A fresh admin deletion runs step 5 in its own transaction, so a newly erased candidate is
  cleared immediately; the sweep later finds only the marker to add.
- Bump only in the #305 PR together with its tests (the version comment in
  `account_erasure_tombstones.go` already says so). This design does not edit it.

## 9. Idempotence, retry, cost

- **Idempotent by construction.** Discovery reads no `ip`; step 5 only changes a non-empty
  `ip` to `''`. Running the erasure twice, interrupting before commit and rerunning, erasing
  `U1` then `U2` or `U2` then `U1` all reach the same final rows (the cleared set does not
  depend on which candidates are already erased; every selector key survives erasure).
- **Cost.** One request query per erasure (section 3), one candidate-row query as today, one
  `UPDATE` per 5000 ids. A conversion has `1 + transferCount` rows; a person's registrations
  bound it. #305 measures the query plan and the sweep page on a seeded table, following the
  existing sweep test that seeds 5000 markers, and records it. If a plan is bad the fix is a
  reviewed index in a new migration (expand-then-contract), not a change to this design.
- **Retry and partial failure.** All work is inside the attempt's transaction. A 55P03 or
  40P01 elsewhere in the pipeline rolls step 5 back with everything else.

## 10. Tests the implementation must pass

All fixtures are built through the real handlers (redeem, admin approve, link confirm, consent,
admin Enable, `rolloverAges`), as `TestAuditErasureTeenRealLifecycles` does, with distinct
documentation-range addresses (inviter `192.0.2.1`, `D` `.2`, redeemer `.3`, guardian `G` `.4`,
enabler `E` `.5`, link confirmer `L` `.6`, consenter `K` `.7`). Hand edits are allowed only in
labelled fallback fixtures, each marked as a simulated anomaly. A "before/after" comparison is
`dumpRows` of every column of the audit rows of the fixture.

**Positive (each asserts which ids changed, their new `ip`, and the counts of section 7)**

- P2-1 **Each selector alone.** For each of K1 to K5, a fixture where `U` is named only by that
  key (K3 by a confirmed link with `teen_user_id = S`; K4 by an account-scope granted row; K5 by
  a `user.enabled` row for `S`). Erasing `U` blanks exactly the group's `1 + transferCount`
  rows that held an address and nothing else; `Blanked` equals that number, `Retained` is `0`.
- P2-2 **L1 (adult person approved).** Erasing `D` blanks `C` and both `T` rows (`192.0.2.2`)
  with G-8 left at its default; `account.approved` is blanked by the *existing* ACTOR rule only.
  Erasing the inviter also blanks the group (K2). Erasing `S` and `G` blank none.
- P2-3 **L2 (teen completes by link decision `L` and consent `K`).** Erasing `D`, the inviter,
  `L` or `K` blanks the group (the completer's address, whoever owns it, per decision 5); erasing
  `G` (who did neither), `E` and `S` blanks none. Repeat with consent recorded by a confirmed
  guardian `G`: then `G` is in the set (K4).
- P2-4 **L3 (birthday, job completion).** The group's addresses are already empty: erasing `D`
  changes no row, `Blanked = 0`, `Retained = 0` (the group is identified). No reason code
  feeds a retention total; `ByReason` still records `history_unestablished: 3` as a
  diagnostic classification (section 7). A variant with mixed empty/non-empty addresses
  asserts `Blanked` equals exactly the non-empty count and per-group `Blanked` names those
  rows.
- P2-5 **L4 and L5 (suspended, enabled by `E`).** Erasing `E` blanks the group (K5). Erasing
  `D` blanks the group too, and `E`'s own `user.enabled` row **keeps** `192.0.2.5`.
- P2-6 **Coincident identities.** `D` also the inviter, `D` also `E`: one group, cleared once,
  counts not doubled.
- P2-7 **Two conversions, one household.** `U` is a candidate of `R1` only: only `R1`'s ids
  change; `R2`'s group, valid and in the same household, is untouched.
- P2-8 **Several candidate requests for one `U`.** `U = D` of `R1` and `L` of `R2`: both
  groups cleared, `Groups = 2`, counts per group are the actual discovered numbers.
- P2-9 **Over-inclusion is real and bounded.** A guardian who confirmed a link and an admin who
  enabled `S` *after* completion are candidates: erasing either blanks the group (accepted),
  and still blanks nothing outside it.

**Negative (over-clearing). Each asserts the row is byte-identical, `ip` included**

- N-1 **Living step rows.** For every erased `U`, the `guardian_link.confirmed`,
  `teen_consent.granted`, `user.enabled`, `account.approved` and `guardian_link.requested`
  (conversion) rows of every *other* person keep their `ip`. `U`'s own step rows change only as
  the existing step 1 rules say.
- N-2 **Not a candidate.** Erasing `S`, a guardian who confirmed/granted nothing, an unrelated
  admin, a user with a revoked-only or `photos`-scope consent, and a user with a *pending*
  (never confirmed) link blanks no group row.
- N-3 **Malformed and ambiguous groups, `U` in the set.** The 6.5.1 test 15 anomalies (15a to
  15c, 15i; the C duplicate with a bad household, the T duplicate missing a shape key, a string
  or `null` identity, all identity keys wrong-typed at `H`), `duplicate_converted_row`,
  `no_converted_row`, `count_mismatch` and a repeated transfer `target_id`: nothing in the
  discovered group changes, valid rows included, and the report gives the actual counts.
  Per-group detail keeps the found counts as retained, and the total `Retained` includes
  every discovered id of the group.
- N-3b **Link-missing approved request (R303-01).** An approved conversion request with
  `decided_by = U`, retained person/invitation links, and `approved_user_id = NULL` (the
  schema permits it; the FK is `ON DELETE SET NULL`): the request is discovered through K1
  (and K2 where the invitation names `U`), its rows are classified `malformed_candidate`
  (G5 fails), nothing in the group changes byte-identically (valid rows included), and
  `Groups`, `Discovered`, `Retained` and `activityEntriesKept` still count it. A second
  variant with `household_person_id = NULL` proves the same. This test fails if the
  request query filters on `approved_user_id IS NOT NULL`.
- N-4 **Household unknown (G4).** The `household_people` row cannot be read: the group is
  retained whole, `HouseholdFallbackSkipped = 1`.
- N-5 **Undiscoverable rows stay.** The 15d variants (a `registration.changed` with all
  identity keys removed; a transfer-shaped row with `target_type = 'event'`) keep their `ip`
  while the valid group is cleared. The report wording claims only "found by the stated rules".
- N-6 **Neighbours.** An ordinary event-target `registration.changed` with `personId = P`, the
  `account.approved` and the conversion `guardian_link.requested` rows of the same request, an
  unrelated conversion in the same household, a non-approved (pending, rejected) conversion
  request, and rows of other actions that happen to carry `192.0.2.2`: all unchanged. The step
  never compares an address value (two groups with the same value and one without a candidate).
- N-7 **No time, no kind.** Flip `household_people.kind`, `users.account_type`, the marker's
  presence and the rows' `at` order in a fixture: the cleared set does not change.
- N-8 **Columns.** After every case above: `action`, `target_type`, `target_id`, `actor_id`,
  `at` and `meta_json` of every audit row, the selector inputs (`decided_by`, `approved_user_id`,
  `invitation_id`, `household_person_id`, `source`, `status`, `confirmed_by`, `granted_by`,
  `invited_by`), the three authorization snapshots and the age-rollover marker are
  byte-identical, `ip` changed on exactly the ids the report lists, and the registration
  access trigger path still passes (`TestAuditErasurePreservesAccessProvenanceTrigger`).
- N-9 **`NULL` and empty `ip`** are neither rewritten nor counted as `Blanked`.
- N-10 **G-8 stays disabled.** No test or code path reads the marker, `accountType` or the
  history to widen the cleared set; the diagnostic classification reasons for identified
  groups are unchanged (`history_unestablished` still recorded in `ByReason`), while no
  reason code feeds a retention total (section 7).
- N-11 **Count contract (R303-02/F-COUNT).** One named test pins every field of section 7
  on a two-group fixture (one identified group with mixed empty/non-empty addresses, one
  retained malformed group) plus an overlapping id shared by two requests: `Groups`,
  `Discovered`, `Retained`, `Blanked`, `ByReason`, each group's `ConvertedRetained`,
  `TransfersRetained` and `Blanked`, the `Retained + |cleared| = Discovered` arithmetic,
  per-group `Blanked` summing to total `Blanked`, and `activityEntriesKept`. It asserts
  actual changed rows are exactly the `RETURNING` ids named per group. On a rerun total
  and per-group `Blanked` and conversion contributions to `IPsBlanked` and `BlankedByClass`
  are zero; other fields are unchanged. Include a fully linked identified request and a
  second approved request with the same person id, `approved_user_id = NULL` and the same
  decider: the latter is non-identified, so their overlapping ids are excluded from `cleared`,
  retained in both details but counted once in the aggregate, and absent from `RETURNING`.
  Any implementation that leaves a
  cleared row counted as retained (or drops a retained row from the counts) fails.

**Idempotence, order and sweep**

- I-1 Erase `U` twice, and again after an interrupted attempt that rolled back: identical rows
  and counts, second `Blanked = 0`, `Retained` unchanged.
- I-2 Erase `D`, then `K`, then `L`, in every order that includes the inviter: identical final
  state.
- I-3 Rollback: an error injected after step 5 leaves every conversion `ip` as before and
  returns no counts (extends `TestAuditErasureConversionRollbackAndOrder`).
- S-1 A version-1 tombstone whose group still has addresses: a version-2 run changes exactly
  the group's `ip`, adds one marker with `version = 2`, at most one `avatar.cleanup`, and
  nothing else in any table (`tombstoneMaintenanceDelta` extended for the blanks).
- S-2 A second version-2 run, and a second daily sweep, are strict no-ops on every table.
- S-3 A tombstone with no P2 rows gets the marker and the cleanup only. A fresh-path
  deletion followed by the version-2 sweep gets the marker and cleanup only.
- S-4 The version appears in the job keys; a version-1 key does not collide; a tombstone
  discovered at version 1 and then version 2 is enqueued once per version.
- S-5 `TestTestFilesSitNextToTheirSource` and the audit writer census still pass; a new source
  guard pins that the only statement writing `ip` of the two conversion actions is the step-5
  statement, that its action/target pairs are fixed strings, and that the candidate query
  contains exactly the K1 to K5 arms (a sixth arm, a time or an address comparison fails the
  build and tells the author to revisit this design).

**Existing tests that change** (each reviewed, none deleted to pass):
`TestAuditErasureTeenRealLifecycles` (asserts today that no conversion `ip` changes for any
of `D`, inviter, `E`, `L`, `K`, `G`, `S`), `TestAuditErasureCoincidentApproverAndInviter`,
`TestAuditErasureConversionFallbackTotalsCountRowsOnce`,
`TestAuditErasureConversionDiscoveryFallbacks`, `TestAuditConversionV0ThroughV4Classification`
(adds `Identified`), and the tombstone version tests that use `tombstoneSweepVersion`.

## 11. Residue statement

Describing a residue item authorizes nothing about keeping it. The owner has not dispositioned
any of these (#304); this table says what P2 reaches *if* implemented as written, and what is
left. "Reached" always means: only `ip`, only on the two actions, only when `U` is in the
candidate set of that conversion and the group is identified.

| Item | What P2 reaches | What remains | Needs |
|---|---|---|---|
| **IR-1** completion rows (`C`/`T` of a later teen completion) | the group's addresses when `U` is a possible caller (K1 to K5) and the group is identified | the group when `U` is a caller no key names (below); the same rows' addresses for every *other* erasure, until that person is erased | owner disposition (#304) |
| **IR-2** transfer rows of an unestablished history | the same groups, with G-8 unestablished, because P2 does not classify | rows of groups not identified; G-8 stays unestablished | owner disposition; G-8 evidence if ever wanted |
| **IR-3** groups retained whole (V0, V1, V2) and malformed conversion `guardian_link.requested` nominations | nothing | every row of such a group, valid rows included; the nominations (not a decision 5 action) | owner disposition |
| **IR-4** rows no retained key reaches | nothing | not countable; includes the text-equality limit (a stored number such as `5.0` is missed when no other arm matches, `TestAuditErasureConversionTextDiscoveryLimit`) and transfer-shaped rows with another `target_type` | owner disposition |
| `api_admin.go` non-Enable completion (current equivalent) | nothing | see below | owner disposition |
| Erased people outside the candidate set | nothing | see below | owner disposition |

**The non-Enable gap, stated conditionally (F-GAP).** What the code shows:
`apiAdminUpdateUser` calls `completeTeenConversionIfReady` with the request's address after
every admin update that reaches its transaction (`api_admin.go:469`), but writes a
`user.enabled` row only when the account was disabled (`:458-463`); the age job passes `""`
(`age_rollover.go:181`). What the code also shows: completion returns early while the
account is administratively disabled (`api_teen_conversion.go:144`). Whether a non-Enable
update can actually be the first completer — i.e. a concrete reachable sequence of account
state, prior link/consent/age facts and the exact update, with `AdministrativelyDisabled`
false at that point — **was not traced at this base and is not claimed here**. The
inventory says the same (6.5.2: "whether such an update can complete a conversion was not
traced"). Conditional residue scenario S-nonEnable: *if* such a completion is reachable,
its caller leaves conversion rows carrying that admin's address with **no key** (not
`decided_by`, not the inviter, not a link or consent row, not `user.enabled`) naming the
admin; erasing that admin is outside the candidate set and IR-1/IR-2 rows stay. P2
deliberately does not add "any admin who edited `S`" to the set: that would be a new
selector and a new product decision. The gap is presented neither as proven nor as closed;
its disposition belongs to the owner in #304.

**Erased people outside the set** (their addresses on `C`/`T` rows stay): the converted
person `S`; a guardian who confirmed no link and granted no consent; an admin whose only
contact with `S` is an edit that left no candidate key (conditional scenario S-nonEnable
above — reachability not traced, disposition to the owner); any person in a conversion
whose group is not identified; any person whose conversion `R` is not `approved` (no rows
exist or none are discoverable); any person whose request link is missing (G5).

**RETAIN_UNATTRIBUTED actions (inventory 6.9, F-RESIDUE).** `household.person_converted`
and conversion-shaped `registration.changed` are class CONVERSION, not RETAIN_*, and are the
only rows P2 can change. **P2 reaches none of the actions below**; each row stays a Q3
residue needing the owner's disposition in #304, not an approved retention. Shapes are the
6.9 shapes, reproduced here so this design stands alone. The action census in 6.9 is
incomplete at this HEAD; the current attribution registry also has the six actions named
below, whose anomalous residue requires the same Q3 disposition.

| Action | Retained shape (all with a non-empty `ip`) |
|---|---|
| `invitation.accepted` | missing, mixed, null or non-numeric identity keys, or an invalid target shape |
| `membership.added_over_limit` | missing or malformed numeric identity keys, or a manager shape without an actor |
| `guardian_link.requested` (unknown or missing source) | no source to attribute |
| `guardian_link.requested` (claim) | claim without actor |
| `guardian_link.requested` (signup) | zero or several matching requests, invalid teen identity, unknown request source, or missing member inviter |
| `guardian_link.requested` (conversion nomination) | invalid target or identity, missing or ambiguous approved request, or missing decider; malformed nominations are counted separately and not added twice |
| `account.requested` | unknown source, or a missing required actor/target |
| `registration.changed` (non-conversion) | without a valid event target or actor (conversion shapes stay in CONVERSION) |
| `registration.cancelled` | inconsistent or malformed actor-null subject identities or target |
| `password.reset_requested` | a non-user target |
| `contact.reason_kept`, `contact.reason_removed`, `add.reason_kept`, `add.reason_removed` | unexpected missing actor on an ordinary ACTOR action |
| `user.erasure_reasons_viewed`, `user.erasure_swept` | unexpected non-empty `ip` on a system action |
| every ordinary ACTOR action with an unexpected missing actor | the writer's `actor_id` is absent, so no ACTOR rule fires; residue on each registered action in the code list below |
| every system-only action with an unexpected non-empty `ip` | a writer that should store no address stored one; residue on each registered action in the code list below |
| any unregistered historical action | any other stored action code with a non-empty `ip`; reported internally by actual code, without treating it as a known writer |

Registered action codes covered by the general shapes (inventory 6.9 list plus the six
current-registry additions above): `account.approved`, `account.expired`, `account.rejected`, `account.requested`,
`account.setup_completed`; `add.completed`, `add.decision`, `add.ended`, `add.expired`,
`add.refused`, `add.requested`, `add.withdrawn`, `add.reason_kept`, `add.reason_removed`; `calendar_token.regenerated`,
`calendar_token.revoked`, `calendar_token.scope_changed`; `contact.decision`,
`contact.ended`, `contact.expired`, `contact.granted`, `contact.privacy_changed`,
`contact.refused`, `contact.requested`, `contact.reveal`, `contact.revoked`,
`contact.withdrawn`, `contact.reason_kept`, `contact.reason_removed`; `contact_history.exported`; `email.change_cancelled`,
`email.change_requested`, `email.changed`, `email.suppression_cleared`; `event.created`,
`event.dates_restored`, `event.deleted`, `event.host_added`, `event.host_removed`,
`event.visibility_changed`, `event.visibility_override`; `group.approved`, `group.deleted`,
`group.rejected`; `guardian_link.confirmed`, `guardian_link.rejected`,
`guardian_link.requested`, `guardian_link.revoked`; `household.account_turned_adult`,
`household.child_turned_13`, `household.created`, `household.person_added`,
`household.person_removed`, `household.person_updated`, `household.updated`;
`invitation.accepted`, `invitation.created`, `invitation.rate_limited`,
`invitation.refused`, `invitation.resent`, `invitation.revoked`; `job.dismissed`,
`job.retried`; `membership.added`, `membership.added_over_limit`,
`membership.not_restored`, `membership.removed`, `membership.restored`,
`membership.role_changed`; `message.cancelled`, `message.edited`, `message.refused`,
`message.retried`, `message.scheduled`, `message.sent`, `message.test_sent`;
`occurrence.blocked`, `occurrence.refused`; `occurrence_access.added`,
`occurrence_access.ended`; `password.changed`, `password.reset_completed`,
`password.reset_requested`; `push.device_on`, `push.keys_reset`, `push.keys_rotated`,
`push.test_sent`; `registration.added`, `registration.added_over_capacity`,
`registration.cancelled`, `registration.changed`, `registration.not_restored`,
`registration.promoted`, `registration.removed`, `registration.restored`;
`room.created`, `room.location_matched`, `room.updated`; `room_booking.booked`,
`room_booking.overlap`, `room_booking.released`, `room_booking.series`;
`room_feed.created`, `room_feed.revoked`; `room_request.confirmed`,
`room_request.rejected`; `setting.church_address_changed`,
`setting.guardian_copies_changed`, `setting.push_enabled_changed`,
`setting.reply_to_changed`, `setting.room_approval_changed`,
`setting.rooms_enabled_changed`, `setting.self_signup_changed`; `teen_consent.granted`,
`teen_consent.revoked`; `user.created`, `user.deleted`, `user.disabled`, `user.erasure_reasons_viewed`, `user.erasure_swept`,
`user.email_changed`, `user.enabled`, `user.role_changed`, `user.turned_18`.
(`household.person_converted` is always CONVERSION and is listed under IR-1 to IR-4, not
here.)

`account.approved`, the conversion `guardian_link.requested` rows and `account_requests.ip`
follow their existing R4-4a classes; P2 changes none of them. No RETAIN_ESSENTIAL action and
no Q4 exception exists (R4-GR-IP), and this design creates none.

## 12. Review gate and record

An approval needs a reviewer who is **not the author of this design**, preferably a different
model, covering privacy, security and correctness against: owner decision 5 verbatim; R4-GR-IP
part 2 (items 11 to 13 and the conversion limits paragraph); over-clearing risk; and this
residue statement. The review names the exact revision (the commit of this file) it read, runs
or reads the SQL of sections 3 and 6 against the schema, and gives an explicit verdict on each of:

1. scope: only `ip`, only the two actions, only the candidate set, nothing of a living
   person's step row (sections 1, 3, 6);
2. the five selectors and the readings in section 13 (K4 narrowing, late-actor
   over-inclusion, and the G5 link condition);
3. the identified-group gate and the fail-closed intersection (section 5), including that
   link-missing requests are discovered but never cleared;
4. the tombstone version-2 transition and its no-op claims (section 8);
5. the count contract of section 7 and the tests of section 10, including that each
   negative case would fail if the implementation over-cleared and that N-11 pins every
   reported field;
6. the residue statement: every item named (IR-1 to IR-4, the conditional S-nonEnable
   scenario, every 6.9 RETAIN_UNATTRIBUTED action and shape), none implying that
   describing it authorizes retention.

Any scope or over-clearing finding blocks. Approval of an exact revision covers that revision
only; a later change to this file needs a new review of what changed.

**Review record:** r1 received two independent reviews with findings (R303-01, R303-02,
F-COUNT, F-GAP, F-RESIDUE, F-LOCK). The r2 review left three findings open. An independent
reviewer approved r3 at commit `ead7e7bc9e1b135ceabc6fd34adfaebabde6bc81`, closing those
findings and confirming the scope, selectors, gates, counts, sweep and residue above. This
record adds no design change; the review of the commit containing it must also cover this
record. The approval does not decide the open matters below.

What an approval would not do: approve #305's code, change a sweep version or a migration,
close G-9, establish G-8, authorize P1, P3, P4 or P5, widen decision 5, or answer Q3. #305 stays
deferred until this design is approved **and** the #300 verification it depends on is satisfied.
This document records the design approval, but not the #300 verification. #304 puts Q3 to Grier only after the residue statement
exists in an approved design. After approval, the inventory (6.5.2, DG-9 items 2 and 3) and
`STATUS.md` are updated by #305's PR, "for the reached residue only", with G-9 still OPEN.

## 13. Judgment calls the reviewer should confirm

- **J1 K4 narrowing** (account scope, granted status). Widening to all `granted_by` rows adds
  speculative cleared rows; narrowing is within decision 5.
- **J2 K2, the inviter, is in the set** because the task and inventory 6.5.2 list it. The inviter
  is the conversion rows' `actor_id`, and may also be an admin who completed the step; the
  reviewer should confirm that "may have caused" covers it, since erasing a plain inviter now
  clears a group that holds `D`'s or a completer's address.
- **J3 Identified groups only (G1 to G5).** Decision 5 does not say whether a malformed,
  ambiguous or link-missing group counts as "identified". This design chooses not to touch
  them. G4 adds one condition to 6.5.1's V0 (household read), because an unread household
  would skip a real check; G5 adds the request-link condition (R303-01), because a NULL
  link escapes the uniqueness indexes and no K-arm or join can name the missing row.
- **J4 Discovery widened to the candidate request set** for the existing counts too, instead of
  a second scan. This changes `Groups`, `Discovered` and `Retained` for erasures of non-deciding
  candidates, and the tests listed in section 10 move with it.
- **J5 No time key.** Late confirmers and late enablers are candidates (P2-9). This is the cost
  of using no time, adjacency or address as a key, which the inventory forbids.
- **J6 Counts: reasons stay diagnostic.** `ByReason` keeps the existing classification codes
  (so `history_unestablished` still appears for cleared groups) while `Retained` alone
  counts retention; per-group `Blanked` is attributed from `RETURNING id`, never estimated.
  An alternative — rewriting a cleared group's reason — would change the existing
  classification contract, so this design does not take it. The reviewer should confirm
  the split in section 7 reads unambiguously.
