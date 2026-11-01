import { useCallback, useEffect, useRef, useState } from "react";
import { Admin } from "../../api";
import { errorText } from "../../lib/errors";
import { Checkbox, FormError } from "../ui/Field";
import { ErrorNotice } from "../ErrorNotice";

// Notifications on devices for the whole church (M3.13c2, DEC-124), on by
// default. Off stops every push, hides the card on Me → Notifications and
// the directions page; members' devices are kept for when it comes back on.
// One switch, so it saves when changed and goes back if saving fails
// (DEC-98), like GuardianCopiesSetting.
export function PushEnabledSetting() {
  const [on, setOn] = useState<boolean | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [saving, setSaving] = useState(false);
  const [ok, setOk] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const alive = useRef(true);

  const load = useCallback(() => {
    setLoadFailed(false);
    Admin.pushEnabled()
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
      const r = await Admin.setPushEnabled(next);
      if (alive.current) { setOn(r.on); setOk(true); }
    } catch (err) {
      if (alive.current) { setOn(previous); setError(errorText(err, "We couldn't save that setting.")); }
    } finally {
      if (alive.current) setSaving(false);
    }
  }

  if (loadFailed) {
    return <div className="mb-6 max-w-xl"><ErrorNotice message="We couldn't load the notifications setting." onRetry={load} /></div>;
  }
  return (
    <div className="mb-6 max-w-xl space-y-2">
      <Checkbox
        label="Notifications on devices"
        description="Members can turn on notifications on their phones and computers. Turn this off to stop all of them; email and Alerts still work. Members' devices stay set up for when you turn it back on."
        checked={on === true}
        disabled={saving || on === null}
        onChange={(e) => void change(e.target.checked)}
      />
      <p role="status" className="text-sm text-success empty:hidden">{ok ? "Saved" : ""}</p>
      {error && <FormError>{error}</FormError>}
    </div>
  );
}
