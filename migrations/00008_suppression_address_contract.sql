-- +goose Up
-- contracts: 00002_suppression_address_hash.sql
-- DEC-138 and PLAN 14.1 contract step for M7.4 R4-SC (T35, PLAN 10.1): a
-- suppression keeps the hash of its address and never the address.
--
-- The expand, 00002, shipped in the v0.1.0 release and added address_hash
-- beside the plaintext address. This file ends the plaintext: it fills any hash
-- still NULL (a row the previous release wrote before 00002 ran, or out-of-band
-- SQL), makes address_hash NOT NULL with a lowercase-hex format CHECK, and
-- drops the address column with its CHECK, its open-row unique index and the
-- hash CHECK that tied the hash to the address. idx_suppressions_open_hash
-- from 00002 stays as the open-row uniqueness and is the webhook's ON CONFLICT
-- target.
--
-- It also removes the same address from the job queue. A webhook.process
-- payload carried the address, and its last_error can quote it (a database
-- error names the key). Each such payload's address becomes addressHash (the
-- SHA-256 of the same bytes, which is what the new binary's job reads, so a
-- report still queued stays valid) and a non-empty last_error is replaced with
-- a fixed sentence. The job rows are locked FOR UPDATE in id order first, and
-- status, attempts, run_at and updated_at are not touched, so retries, the
-- 30-day pruning of finished jobs and the dead-job list behave as before.
-- detail is the provider's short category name ("HardBounce"), which
-- suppressions.detail keeps, so it stays in the payload.
--
-- Everything here runs in one goose transaction. LOCK: ADD CONSTRAINT and DROP
-- COLUMN take ACCESS EXCLUSIVE on suppressions and hold it until commit, so
-- every mail send waits for the whole file; the UPDATEs add row locks on
-- suppressions and on the webhook.process rows. The shipped upgrade
-- (provision.sh `docker compose up -d`) stops the old app before the new one
-- migrates, so nothing waits. A hand-run `fold migrate up` against a live
-- server blocks those reads. lock_timeout bounds each wait to acquire a lock,
-- not the hold; the span on a large table is measured by
-- TestSuppressionAddressContractScale and recorded in the release PR.
--
-- Down cannot restore addresses that were not kept. It refuses while any
-- suppression exists, and otherwise puts the old shape back on an empty table;
-- webhook payloads and error texts stay as scrubbed. A rollback to the previous
-- release restores the dump `upgrade.sh` takes before an upgrade
-- (docs/DEPLOY-DIGITALOCEAN.md section 6), not this Down.
SET LOCAL lock_timeout = '5s';

UPDATE suppressions SET address_hash = encode(sha256(convert_to(address, 'UTF8')), 'hex') WHERE address_hash IS NULL;

WITH locked AS (
    SELECT id FROM jobs WHERE kind = 'webhook.process' ORDER BY id FOR UPDATE
)
UPDATE jobs j SET
    payload = CASE
        WHEN j.payload ? 'address' THEN (j.payload - 'address') || CASE
            WHEN jsonb_typeof(j.payload -> 'address') = 'string'
                THEN jsonb_build_object('addressHash', encode(sha256(convert_to(j.payload ->> 'address', 'UTF8')), 'hex'))
            ELSE '{}'::jsonb END
        ELSE j.payload END,
    last_error = CASE WHEN j.last_error = '' THEN '' ELSE 'details removed when email addresses stopped being stored' END
FROM locked
WHERE j.id = locked.id;

ALTER TABLE suppressions ALTER COLUMN address_hash SET NOT NULL;
ALTER TABLE suppressions ADD CONSTRAINT suppressions_address_hash_format_check CHECK (address_hash ~ '^[0-9a-f]{64}$');
DROP INDEX idx_suppressions_open;
ALTER TABLE suppressions DROP CONSTRAINT suppressions_address_hash_check;
ALTER TABLE suppressions DROP CONSTRAINT suppressions_address_check;
ALTER TABLE suppressions DROP COLUMN address;

-- +goose Down
-- Refuses while any suppression exists: the addresses are gone, and a column
-- rebuilt from hashes would be a lie. Restore the upgrade's dump instead.
SET LOCAL lock_timeout = '5s';
LOCK TABLE suppressions IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM suppressions) THEN
        RAISE EXCEPTION 'suppression addresses are not stored, so down would lose data; restore the upgrade dump; refusing';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE suppressions ADD COLUMN address text NOT NULL;
ALTER TABLE suppressions ALTER COLUMN address_hash DROP NOT NULL;
ALTER TABLE suppressions DROP CONSTRAINT suppressions_address_hash_format_check;
ALTER TABLE suppressions ADD CONSTRAINT suppressions_address_check
    CHECK (((address = lower(btrim(address))) AND ((length(address) >= 3) AND (length(address) <= 320))));
ALTER TABLE suppressions ADD CONSTRAINT suppressions_address_hash_check
    CHECK (address_hash IS NULL OR address_hash = encode(sha256(convert_to(address, 'UTF8')), 'hex'));
CREATE UNIQUE INDEX idx_suppressions_open ON suppressions (channel, address, reason) WHERE cleared_at IS NULL;
