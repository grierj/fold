import { useCallback, useEffect, useRef, useState } from "react";
import { NotificationSettings } from "../api";
import { errorText } from "../lib/errors";
import { Checkbox, FormError } from "./ui/Field";
import { Skeleton } from "./ui/Skeleton";
import { ErrorNotice } from "./ErrorNotice";

// Me > Notifications (DEC-97). The switch saves the moment it changes
// (DEC-98): it is disabled while a save is in flight, and a failed save puts
// the box back and says why. The page's own heading comes from Me.
export function NotificationSettingsCard() {
  const [off, setOff] = useState<boolean | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const alive = useRef(true);

  const load = useCallback(() => {
    setLoadError(null);
    NotificationSettings.get()
      .then((r) => { if (alive.current) setOff(r.settings.optionalEmailOff); })
      .catch((err) => { if (alive.current) setLoadError(errorText(err, "We couldn't load your email settings.")); });
  }, []);

  useEffect(() => {
    alive.current = true;
    load();
    return () => { alive.current = false; };
  }, [load]);

  async function change(next: boolean) {
    const previous = off;
    setSaving(true); setOff(next); setSaved(false); setError(null);
    try {
      const r = await NotificationSettings.update(next);
      if (alive.current) { setOff(r.settings.optionalEmailOff); setSaved(true); }
    } catch (err) {
      if (alive.current) { setOff(previous); setError(errorText(err, "We couldn't save your changes.")); }
    } finally { if (alive.current) setSaving(false); }
  }

  if (loadError) return <ErrorNotice message={loadError} onRetry={load} />;
  if (off === null) return <Skeleton kind="lines" count={1} label="Loading your email settings…" />;
  return <div className="bg-surface rounded-lg border border-border p-6 space-y-3">
    {error && <FormError>{error}</FormError>}
    <Checkbox label="Turn off all optional email" description="Stops emails about group and event updates, waitlist changes and leader changes. You'll still get emails about your account, safety and requests waiting on you, and everything still shows in Alerts." checked={off} disabled={saving} onChange={(e) => void change(e.target.checked)} />
    <p role="status" className="text-success text-sm">{saved ? "Saved" : ""}</p>
  </div>;
}
