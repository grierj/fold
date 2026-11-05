-- +goose Up
-- M7.4 R4-2d1, owner decision 1 (inventory T05 and section 7.2). Account erasure
-- must blank the ip, user_agent and evidence_text of a deleted owner's consent
-- rows, and the baseline trigger refuses every UPDATE. This migration lets
-- reject_consent_change() allow exactly that scrub and nothing else.
--
-- The trigger allows an UPDATE only when the owner is soft-deleted, the three
-- fields end up empty, and every other column (compared as a whole row, so a
-- future column stays protected) is unchanged. Everything else, and every
-- DELETE, raises the same 'consent history is append-only' error as before.
--
-- DEC-138 and PLAN 14.1: this is an additive expand step. It replaces a function
-- body and drops or renames nothing the previous release reads or writes. The
-- previous release never updates consents, so it keeps working. It is not a
-- contract migration, and no later migration needs to name it.
--
-- LOCK: CREATE OR REPLACE FUNCTION takes no lock on consents, and the trigger
-- consents_append_only is unchanged. Consent reads and inserts do not wait for
-- this migration. lock_timeout bounds only the wait to acquire a lock, not the
-- hold.
SET LOCAL lock_timeout = '5s';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.reject_consent_change() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF NEW.ip = '' AND NEW.user_agent = '' AND NEW.evidence_text = ''
           AND (to_jsonb(NEW) - 'ip' - 'user_agent' - 'evidence_text') = (to_jsonb(OLD) - 'ip' - 'user_agent' - 'evidence_text')
           AND EXISTS (SELECT 1 FROM public.users WHERE id = OLD.user_id AND deleted_at IS NOT NULL) THEN
            RETURN NEW;
        END IF;
    END IF;
    RAISE EXCEPTION 'consent history is append-only';
END;
$$;
-- +goose StatementEnd

-- +goose Down
SET LOCAL lock_timeout = '5s';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.reject_consent_change() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    RAISE EXCEPTION 'consent history is append-only';
END;
$$;
-- +goose StatementEnd
