import { useCallback, useEffect, useRef, useState } from "react";
import { Admin } from "../../api";
import { errorCode, errorText } from "../../lib/errors";
import { useUnsavedChanges } from "../../lib/unsavedChanges";
import { Form } from "../ui/Form";
import { Checkbox, Field, FormError, Input } from "../ui/Field";
import { FormActions } from "../ui/FormActions";
import { Button } from "../ui/Button";
import { ErrorNotice } from "../ErrorNotice";

// The two church settings announcements read (M3.5b): guardian copies of
// messages to teens (DEC-19) and where replies go (DEC-130). Each is
// audited on the server.

// GuardianCopiesSetting is one on/off switch, so it saves when changed,
// says "Saved", and goes back if saving fails (DEC-98).
export function GuardianCopiesSetting() {
  const [on, setOn] = useState<boolean | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [saving, setSaving] = useState(false);
  const [ok, setOk] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const alive = useRef(true);

  const load = useCallback(() => {
    setLoadFailed(false);
    Admin.guardianCopies()
      .then((r) => { if (alive.current) setOn(r.on); })
      .catch(() => { if (alive.current) setLoadFailed(true); });
  }, []);

  useEffect(() => {
    alive.current = true;
    load();
    return () => { alive.current = false; };
  }, [load]);

  async function change(next: boolean) {
    const previous = on;
    setSaving(true);
    setOn(next);
    setError(null);
    setOk(false);
    try {
      const r = await Admin.setGuardianCopies(next);
      setOn(r.on);
      setOk(true);
    } catch (err) {
      setOn(previous);
      setError(errorText(err, "We couldn't save that setting."));
    } finally {
      setSaving(false);
    }
  }

  if (loadFailed) {
    return <div className="mb-6 max-w-xl"><ErrorNotice message="We couldn't load the guardian copies setting." onRetry={load} /></div>;
  }
  return (
    <div className="mb-6 max-w-xl space-y-2">
      <Checkbox
        label="Send guardians a copy of messages to their teens"
        description="When a message goes to a teen, their confirmed parents or guardians also get an email copy, marked as a copy. Takes effect for messages sent from now on. Off by default."
        checked={on === true}
        disabled={saving || on === null}
        onChange={(e) => void change(e.target.checked)}
      />
      <p role="status" className="text-sm text-success empty:hidden">{ok ? "Saved" : ""}</p>
      {error && <FormError>{error}</FormError>}
    </div>
  );
}

// ReplyToSetting is where replies to announcements go until the reply
// relay (M3.11): one address, usually the church office's, or none. A text
// field, so it keeps a Save button (DEC-98).
export function ReplyToSetting() {
  const [address, setAddress] = useState("");
  const [saved, setSaved] = useState<string | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [ok, setOk] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [fieldError, setFieldError] = useState<string | null>(null);
  const alive = useRef(true);
  const { guard } = useUnsavedChanges(saved !== null && address.trim() !== saved);

  const load = useCallback(() => {
    setLoadFailed(false);
    Admin.replyTo()
      .then((r) => { if (alive.current) { setAddress(r.address); setSaved(r.address); } })
      .catch(() => { if (alive.current) setLoadFailed(true); });
  }, []);

  useEffect(() => {
    alive.current = true;
    load();
    return () => { alive.current = false; };
  }, [load]);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setError(null); setOk(false); setFieldError(null);
    setBusy(true);
    try {
      const r = await Admin.setReplyTo(address.trim());
      setAddress(r.address); setSaved(r.address); setOk(true);
    } catch (err) {
      const text = errorText(err, "We couldn't save where replies go. Try again.");
      if (errorCode(err) === "reply_to_invalid") setFieldError(text);
      else setError(text);
    } finally { setBusy(false); }
  }

  if (loadFailed) {
    return <div className="mb-6 max-w-xl"><ErrorNotice message="We couldn't load where replies go." onRetry={load} /></div>;
  }
  return (
    <Form onSubmit={save} className="mb-6 max-w-xl space-y-4">
      {guard}
      <Field id="reply-to" label="Where replies go" optional error={fieldError ?? undefined}
        hint="Replies to announcements go here, usually the church office. Leave empty for no reply address.">
        <Input type="email" value={address} maxLength={254} autoComplete="email"
          onChange={(e) => { setAddress(e.target.value); setOk(false); setFieldError(null); }} />
      </Field>
      <FormActions>
        <Button variant="secondary" layout="w-full sm:w-auto" type="submit" disabled={busy || saved === null || address.trim() === saved}>
          {busy ? "Saving…" : "Save reply address"}
        </Button>
        {ok && <span role="status" className="text-sm text-success">Saved</span>}
      </FormActions>
      {error && <FormError>{error}</FormError>}
    </Form>
  );
}
