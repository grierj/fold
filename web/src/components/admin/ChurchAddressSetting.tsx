import { useEffect, useState } from "react";
import { Admin } from "../../api";
import { errorText } from "../../lib/errors";
import { useUnsavedChanges } from "../../lib/unsavedChanges";
import { Form } from "../ui/Form";
import { Field, FormError, Textarea } from "../ui/Field";
import { FormActions } from "../ui/FormActions";
import { Button } from "../ui/Button";

// The church's mailing address for the footer of optional email (DEC-116,
// PLAN 10.5). A text field, so it keeps a Save button (DEC-98). Until it is
// set, a one-line reminder sits above the field.
export function ChurchAddressSetting() {
  const [address, setAddress] = useState("");
  const [saved, setSaved] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [ok, setOk] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const { guard } = useUnsavedChanges(saved !== null && address.trim() !== saved);

  useEffect(() => {
    let alive = true;
    Admin.churchAddress()
      .then((r) => { if (alive) { setAddress(r.address); setSaved(r.address); } })
      .catch(() => { if (alive) setSaved(""); });
    return () => { alive = false; };
  }, []);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true); setError(null); setOk(false);
    try {
      const r = await Admin.setChurchAddress(address);
      setAddress(r.address); setSaved(r.address); setOk(true);
    } catch (err) {
      setError(errorText(err, "We couldn't save the address."));
    } finally { setBusy(false); }
  }

  return (
    <Form noValidate onSubmit={save} className="mb-6 max-w-xl space-y-4">
      {guard}
      {saved === "" && <p className="text-sm text-text-muted">Add your church's mailing address so optional emails can include it.</p>}
      <Field id="church-address" label="Church mailing address" optional hint="Shown at the bottom of optional emails, as the law asks. Up to 6 lines.">
        <Textarea rows={3} value={address} maxLength={300} autoComplete="street-address"
          onChange={(e) => { setAddress(e.target.value); setOk(false); }} />
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
