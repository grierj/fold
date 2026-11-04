-- +goose Up
-- DEC-138 and PLAN 14.1 expand step for M7.4 R4-S. address_hash is the lowercase
-- hex SHA-256 of the stored address's UTF-8 bytes. Fold stores addresses already
-- normalized in Go, so on a UTF8 database the value equals mail.AddressHash of
-- any input that normalizes to that address. The plaintext address,
-- suppressions_address_check and idx_suppressions_open are kept on purpose for
-- the previous release.
--
-- LOCK: goose runs this file in one transaction. ADD COLUMN takes ACCESS
-- EXCLUSIVE on suppressions and holds it through the UPDATE, the CHECK
-- validation and the index build until commit, so every suppression read (each
-- mail send) waits for the whole migration. The shipped upgrade (provision.sh
-- `docker compose up -d`) stops the old app before the new one migrates, so
-- nothing waits. A hand-run `fold migrate up` against a live server blocks
-- those reads. lock_timeout bounds only the wait to acquire the lock, not the
-- hold.
--
-- The contract migration (R4-SC) must name this file, and it needs an earlier
-- tagged and published release that contains this file. Never edit this file
-- after merge.
SET LOCAL lock_timeout = '5s';
ALTER TABLE suppressions ADD COLUMN address_hash text;
UPDATE suppressions SET address_hash = encode(sha256(convert_to(address, 'UTF8')), 'hex');
ALTER TABLE suppressions ADD CONSTRAINT suppressions_address_hash_check
    CHECK (address_hash IS NULL OR address_hash = encode(sha256(convert_to(address, 'UTF8')), 'hex'));
-- Safe now: open (channel, address, reason) is already unique, and the CHECK
-- makes the hash a function of the address, so no two open rows can share
-- (channel, address_hash, reason). NULL hashes do not collide.
CREATE UNIQUE INDEX idx_suppressions_open_hash ON suppressions (channel, address_hash, reason) WHERE cleared_at IS NULL;

-- +goose Down
SET LOCAL lock_timeout = '5s';
DROP INDEX idx_suppressions_open_hash;
ALTER TABLE suppressions DROP CONSTRAINT suppressions_address_hash_check;
ALTER TABLE suppressions DROP COLUMN address_hash;
