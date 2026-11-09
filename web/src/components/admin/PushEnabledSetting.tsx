import { Admin } from "../../api/admin";
import { invalidateSite } from "../../lib/useSite";
import { useLoad } from "../../lib/useLoad";
import { ErrorNotice } from "../ErrorNotice";
import { SettingSwitch } from "./SettingSwitch";

// Notifications on devices for the whole church (M3.13c2, DEC-124), on by
// default. Off stops every push, hides the card on Me → Notifications and
// the directions page; members' devices are kept for when it comes back on.
export function PushEnabledSetting() {
  const setting = useLoad(() => Admin.pushEnabled(), []);
  if (setting.error) {
    return <div className="mb-6 max-w-xl"><ErrorNotice message="We couldn't load the notifications setting." onRetry={setting.retry} /></div>;
  }
  return (
    <SettingSwitch
      label="Notifications on devices"
      description="Members can turn on notifications on their phones and computers. Turn this off to stop all of them; email and Alerts still work. Members' devices stay set up for when you turn it back on."
      value={setting.data ? setting.data.on : null}
      save={async (next) => {
        const r = await Admin.setPushEnabled(next);
        invalidateSite({ pushEnabled: r.on });
        setting.setData({ on: r.on });
      }}
    />
  );
}
