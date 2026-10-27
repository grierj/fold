-- +goose Up
-- A membership row is reused after departure, but each active tenure is a
-- distinct authorization lifecycle. Preserve joined_at as the first-ever join
-- time and advance this discriminator whenever a removed row becomes active.
ALTER TABLE memberships
    ADD COLUMN tenure bigint NOT NULL DEFAULT 1 CHECK (tenure > 0);

-- Keep every membership writer consistent, including maintenance and future
-- paths that update status without going through the HTTP role-change helper.
-- +goose StatementBegin
CREATE FUNCTION guard_membership_tenure() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.tenure IS DISTINCT FROM OLD.tenure THEN
        RAISE EXCEPTION 'membership tenure is managed by status transitions' USING ERRCODE = '23514';
    END IF;
    IF OLD.status IS DISTINCT FROM 'active' AND NEW.status = 'active' THEN
        NEW.tenure := OLD.tenure + 1;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER memberships_tenure_guard
    BEFORE UPDATE ON memberships
    FOR EACH ROW EXECUTE FUNCTION guard_membership_tenure();

-- +goose Down
-- Downgrading can only preserve a membership whose lifecycle discriminator has
-- never advanced. Lock before checking so an in-flight reactivation either
-- commits first and is refused here, or remains blocked until rollback ends.
LOCK TABLE memberships IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM memberships WHERE tenure <> 1) THEN
        RAISE EXCEPTION 'advanced membership tenure exists; cannot downgrade safely';
    END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER memberships_tenure_guard ON memberships;
DROP FUNCTION guard_membership_tenure();
ALTER TABLE memberships DROP COLUMN tenure;
