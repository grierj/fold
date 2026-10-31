import { NotificationSettings } from "../api";
import type { EmailSettingsChange } from "../api";
import { EmailSettings } from "./EmailSettings";

const load = () => NotificationSettings.get().then((r) => r.settings);
const save = (c: EmailSettingsChange) => NotificationSettings.update(c).then((r) => r.settings);

// Me > Notifications (DEC-97, DEC-115): the shared email switches, saved
// with the person's session. The page's own heading comes from Me.
export function NotificationSettingsCard() {
  return <EmailSettings load={load} save={save} />;
}
