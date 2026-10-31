import { useCallback, useEffect, useRef, useState } from "react";
import type { EmailSettingsChange } from "../api";
import type { EmailKind, NotificationSettings } from "../types";
import { errorText } from "../lib/errors";
import { Checkbox, Choices, FormError } from "./ui/Field";
import { Skeleton } from "./ui/Skeleton";
import { ErrorNotice } from "./ErrorNotice";

// What each optional kind of email is called, with a line about what it is
// (DEC-115). The order is the order the server lists them in.
const EMAIL_KIND_COPY: Record<EmailKind["category"], { label: string; hint: string }> = {
  groups: { label: "Group updates", hint: "When your groups change or are approved" },
  events: { label: "Event updates", hint: "Cancelled or restored dates you can see" },
  messages: { label: "Message alerts", hint: "When someone sends you a message in Fold" },
  admin: { label: "Admin notices", hint: "For church admins" },
};

// The email switches (DEC-115), shared by Me > Notifications and the email
// settings page an email's link opens: one per optional kind plus "Turn off
// all optional email". Each saves the moment it changes (DEC-98); every
// switch is disabled while a save is in flight, and a failed save puts the
// switches back and says why. The two pages differ only in how they load
// and save.
export function EmailSettings({ load, save, first }: {
  load: () => Promise<NotificationSettings>;
  save: (change: EmailSettingsChange) => Promise<NotificationSettings>;
  // The kind the person came from (an email's link), listed first.
  first?: string;
}) {
  const [settings, setSettings] = useState<NotificationSettings | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const alive = useRef(true);

  const reload = useCallback(() => {
    setLoadError(null);
    load()
      .then((s) => { if (alive.current) setSettings(s); })
      .catch((err) => { if (alive.current) setLoadError(errorText(err, "We couldn't load your email settings.")); });
  }, [load]);

  useEffect(() => {
    alive.current = true;
    reload();
    return () => { alive.current = false; };
  }, [reload]);

  async function change(c: EmailSettingsChange) {
    if (!settings) return;
    const previous = settings;
    // Show the change at once; the server's answer is the truth.
    setSettings({
      optionalEmailOff: c.category ? previous.kinds.every((k) => (k.category === c.category ? c.off : k.off)) : c.off,
      kinds: previous.kinds.map((k) => (!c.category || k.category === c.category ? { ...k, off: c.off } : k)),
    });
    setSaving(true); setSaved(false); setError(null);
    try {
      const next = await save(c);
      if (alive.current) { setSettings(next); setSaved(true); }
    } catch (err) {
      if (alive.current) { setSettings(previous); setError(errorText(err, "We couldn't save your changes.")); }
    } finally { if (alive.current) setSaving(false); }
  }

  if (loadError) return <ErrorNotice message={loadError} onRetry={reload} />;
  if (!settings) return <Skeleton kind="lines" count={3} label="Loading your email settings…" />;
  const kinds = [...settings.kinds].sort((a, b) => Number(b.category === first) - Number(a.category === first));
  return <div className="bg-surface rounded-lg border border-border p-6 space-y-4">
    {error && <FormError>{error}</FormError>}
    <Choices legend="Optional email" hint="Turn off the kinds you don't want. Everything still shows in Alerts.">
      {kinds.map((k) => {
        const copy = EMAIL_KIND_COPY[k.category];
        return <Checkbox key={k.category} label={copy.label} description={copy.hint} checked={!k.off} disabled={saving}
          onChange={(e) => void change({ category: k.category, off: !e.target.checked })} />;
      })}
    </Choices>
    <Checkbox label="Turn off all optional email" description="Stops every kind above at once." checked={settings.optionalEmailOff} disabled={saving}
      onChange={(e) => void change({ off: e.target.checked })} />
    <p className="text-sm text-text-muted">Account and safety email always comes: password resets, sign-in links, and changes to events you're going to.</p>
    <p role="status" className="text-success text-sm">{saved ? "Saved" : ""}</p>
  </div>;
}
