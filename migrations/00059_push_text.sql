-- +goose Up
-- Web push delivery (M3.13b, DEC-124). A notification's lock-screen text is
-- its kind's own short PushRender, rendered when the row is written (the
-- same Vars as the bell, so the caller's visibility decisions are in it).
-- Empty push_title means the kind doesn't push.
ALTER TABLE notifications ADD COLUMN push_title text NOT NULL DEFAULT '';
ALTER TABLE notifications ADD COLUMN push_body text NOT NULL DEFAULT '';

-- A person's devices end with their sessions (research 2.8): whatever
-- revokes every session (a password change or reset, an email or role
-- change, deletion) or takes the account out of active (disabled, waiting
-- for consent) removes every push device, so a phone stops getting pushes
-- meant for the account. Logout removes only its own browser's device, in
-- the logout request.
-- +goose StatementBegin
CREATE FUNCTION push_devices_end_with_sessions() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM push_subscriptions WHERE user_id = NEW.id;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER push_devices_end_with_sessions AFTER UPDATE OF session_version, status ON users
    FOR EACH ROW
    WHEN (NEW.session_version IS DISTINCT FROM OLD.session_version
        OR (NEW.status IS DISTINCT FROM OLD.status AND NEW.status <> 'active'))
    EXECUTE FUNCTION push_devices_end_with_sessions();

-- +goose Down
DROP TRIGGER push_devices_end_with_sessions ON users;
DROP FUNCTION push_devices_end_with_sessions();
ALTER TABLE notifications DROP COLUMN push_body;
ALTER TABLE notifications DROP COLUMN push_title;
