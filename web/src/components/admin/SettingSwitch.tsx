import { useState, type ReactNode } from "react";
import { errorText } from "../../lib/errors";
import { Checkbox, FormError } from "../ui/Field";

// A setting that saves the moment it changes and goes back if saving fails
// (DEC-98). While a save is in flight the new choice shows, the control is off,
// and "Saved" or the error follows.
export function useSavedSetting<T>() {
  const [pending, setPending] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [ok, setOk] = useState(false);
  const [saving, setSaving] = useState(false);
  return {
    pending, saving, ok, error,
    async change(next: T, save: (next: T) => Promise<void>) {
      // The control is disabled while a save is in flight, so changes are one at
      // a time and a failure goes back to the value from before this save.
      setSaving(true);
      setPending(next);
      setError(null);
      setOk(false);
      try {
        await save(next);
        setOk(true);
      } catch (err) {
        setError(errorText(err, "We couldn't save that setting."));
      } finally {
        setPending(null);
        setSaving(false);
      }
    },
  };
}

export function SavedStatus({ ok, error }: { ok: boolean; error: string | null }) {
  return <>
    <p role="status" className="text-sm text-success empty:hidden">{ok ? "Saved" : ""}</p>
    {error && <FormError>{error}</FormError>}
  </>;
}

// One on/off setting as a checkbox. `value` is null until it is known (the
// control stays off); `save` throws to go back.
export function SettingSwitch({ label, description, value, save }: {
  label: string;
  description: ReactNode;
  value: boolean | null;
  save: (next: boolean) => Promise<void>;
}) {
  const setting = useSavedSetting<boolean>();
  return (
    <div className="mb-6 max-w-xl space-y-2">
      <Checkbox
        label={label}
        description={description}
        checked={setting.pending ?? value === true}
        disabled={setting.saving || value === null}
        onChange={(e) => void setting.change(e.target.checked, save)}
      />
      <SavedStatus ok={setting.ok} error={setting.error} />
    </div>
  );
}
