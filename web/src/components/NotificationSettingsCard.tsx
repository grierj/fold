import { useState } from "react";
import { NotificationSettings } from "../api";
import type { EmailSettingsChange } from "../api";
import type { NotificationSettings as Settings } from "../types";
import { EmailSettings } from "./EmailSettings";
import { PushDeviceCard } from "./PushDeviceCard";

const load = () => NotificationSettings.get().then((r) => r.settings);
const save = (c: EmailSettingsChange) => NotificationSettings.update(c).then((r) => r.settings);

// sharedFirst hands one request to its first caller and a fresh one to
// every later call (a retry).
function sharedFirst(first: Promise<Settings>): () => Promise<Settings> {
  let used = false;
  return () => {
    if (used) return load();
    used = true;
    return first;
  };
}

// Me > Notifications (DEC-97, DEC-115): the shared email switches, saved
// with the person's session, and Notifications on this device (M3.13c1)
// with its own Push switches. The page's own heading comes from Me. Both
// cards read the same settings, so the first load is one request.
export function NotificationSettingsCard() {
  const [loaders] = useState(() => {
    const first = load();
    return { email: sharedFirst(first), push: sharedFirst(first) };
  });
  return <div className="space-y-6">
    <EmailSettings load={loaders.email} save={save} />
    <PushDeviceCard load={loaders.push} />
  </div>;
}
