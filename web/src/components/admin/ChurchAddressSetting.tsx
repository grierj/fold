import { useCallback, useEffect, useRef, useState } from "react";
import { Admin } from "../../api";
import { errorText } from "../../lib/errors";
import { useUnsavedChanges } from "../../lib/unsavedChanges";
import { Form } from "../ui/Form";
import { Field, FormError, Textarea } from "../ui/Field";
import { FormActions } from "../ui/FormActions";
import { Button } from "../ui/Button";
import { ErrorNotice } from "../ErrorNotice";

const MAX_LINES = 6;

// Lines the server keeps: blank ones are dropped.
const lines = (s: string) => s.split("\n").filter((l) => l.trim() !== "").length;

// The church's mailing address for the footer of optional email (DEC-116,
// PLAN 10.5). A text field, so it keeps a Save button (DEC-98). Until it is
// set, a one-line reminder sits above the field; a failed load says so with
// a retry instead.
export function ChurchAddressSetting() {
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
    Admin.churchAddress()
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
    setError(null); setOk(false);
    if (lines(address) > MAX_LINES) {
      setFieldError(`Keep the address to ${MAX_LINES} lines.`);
      return;
    }
    setFieldError(null);
    setBusy(true);
    try {
      const r = await Admin.setChurchAddress(address);
      setAddress(r.address); setSaved(r.address); setOk(true);
    } catch (err) {
      setError(errorText(err, "We couldn't save the address. Try again."));
    } finally { setBusy(false); }
  }

  if (loadFailed) {
    return <div className="mb-6 max-w-xl"><ErrorNotice message="We couldn't load the church's mailing address." onRetry={load} /></div>;
  }
  return (
    <Form noValidate onSubmit={save} className="mb-6 max-w-xl space-y-4">
      {guard}
      {saved === "" && <p className="text-sm text-text-muted">Add your church's mailing address so optional emails can include it.</p>}
      <Field id="church-address" label="Church mailing address" optional error={fieldError ?? undefined}
        hint="Shown at the bottom of optional emails, as the law asks. Up to 6 lines.">
        <Textarea rows={3} value={address} maxLength={300} autoComplete="street-address"
          onChange={(e) => { setAddress(e.target.value); setOk(false); setFieldError(null); }} />
      </Field>
      <FormActions>
        <Button variant="secondary" layout="w-full sm:w-auto" type="submit" disabled={busy || saved === null || address.trim() === saved}>
          {busy ? "Saving…" : "Save address"}
        </Button>
        {ok && <span role="status" className="text-sm text-success">Saved</span>}
      </FormActions>
      {error && <FormError>{error}</FormError>}
    </Form>
  );
}
