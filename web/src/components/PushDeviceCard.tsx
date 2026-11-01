import { useEffect, useRef, useState } from "react";
import { NotificationSettings as SettingsApi, Push } from "../api";
import type { EmailKind, NotificationSettings, PushDevice, PushSettings, PushSubscribeResponse } from "../types";
import { errorText } from "../lib/errors";
import { cardState, deviceState, localSubscription, readPushEnv, sameKey, subscribeThisDevice, type Platform } from "../lib/pushState";
import { useSiteName } from "../lib/useSiteName";
import { formatShortDay } from "../lib/when";
import { Button, ButtonLink } from "./ui/Button";
import { Checkbox, Choices, FormError } from "./ui/Field";
import { Skeleton } from "./ui/Skeleton";
import { ErrorNotice } from "./ErrorNotice";
import { EMAIL_KIND_COPY } from "./EmailSettings";

// The directions page (M3.13c2) for each platform's steps and undo steps.
const SETUP = "/notifications/setup";

// Re-saving this browser's subscription on load fixes a device the server
// dropped (a password, email or role change deletes them, M3.13b) while the
// browser kept its subscription. At most one PUT per endpoint per page load:
// the result is kept for later visits to this page.
const resaved = new Map<string, Promise<PushSubscribeResponse | null>>();
function resaveOnce(sub: PushSubscription): Promise<PushSubscribeResponse | null> {
  let p = resaved.get(sub.endpoint);
  if (!p) {
    p = Push.save(sub.toJSON()).catch(() => null);
    resaved.set(sub.endpoint, p);
  }
  return p;
}

const NO_PUSH: PushSettings = { on: false, devices: [], kinds: [] };

interface Loaded {
  enabled: boolean;
  publicKey: string;
  platform: Platform;
  device: ReturnType<typeof deviceState>;
  // This browser's subscription, when the server has it as a device.
  mine: { id: number; sub: PushSubscription } | null;
  // Permission was granted here before, but the device isn't on now.
  setUpBefore: boolean;
  push: PushSettings;
}

// Notifications on this device (M3.13c1, research 2.7, DEC-127): the card on
// Me → Notifications. "On" for this browser means it has a local push
// subscription and saving it again succeeds; the settings never return
// endpoints, so the browser can't look itself up in the device list.
export function PushDeviceCard({ load }: { load: () => Promise<NotificationSettings> }) {
  const site = useSiteName();
  const [data, setData] = useState<Loaded | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("");
  const [error, setError] = useState<string | null>(null);
  const alive = useRef(true);
  // The button pressed is gone after turning on or off, so focus moves to
  // the card heading instead of falling to the page.
  const heading = useRef<HTMLHeadingElement>(null);

  useEffect(() => {
    alive.current = true;
    setLoadError(null);
    (async () => {
      try {
        const pending = load();
        pending.catch(() => {}); // a failure is shown below, or not at all when the church is off
        const config = await Push.config();
        if (!config.enabled) {
          if (alive.current) setData({ enabled: false, publicKey: "", platform: "computer", device: "unavailable", mine: null, setUpBefore: false, push: NO_PUSH });
          return;
        }
        const settings = await pending;
        const env = readPushEnv();
        const device = deviceState(env);
        const push = settings.push ?? NO_PUSH;
        let mine: Loaded["mine"] = null;
        let setUpBefore = false;
        if (device === "available" && env.permission === "granted") {
          const sub = await localSubscription();
          // Re-save only while the person's push consent is on: after
          // "Turn off notifications everywhere" on another device, this
          // browser's leftover subscription must not turn it back on.
          if (sub && push.on && sameKey(sub, config.publicKey)) {
            const r = await resaveOnce(sub);
            if (r?.saved && r.device) mine = { id: r.device.id, sub };
          }
          setUpBefore = !!sub && !mine;
        }
        if (alive.current) setData({ enabled: true, publicKey: config.publicKey, platform: env.platform, device, mine, setUpBefore, push });
      } catch (err) {
        if (alive.current) setLoadError(errorText(err, "We couldn't load your notification settings."));
      }
    })();
    return () => { alive.current = false; };
  }, [load, attempt]);

  if (loadError) return <ErrorNotice message={loadError} onRetry={() => setAttempt((n) => n + 1)} />;
  if (!data) return <Skeleton kind="lines" count={2} label="Loading notifications on this device…" />;
  const others = data.push.devices.filter((d) => d.id !== data.mine?.id);
  const state = cardState({ enabled: data.enabled, device: data.device, thisDeviceOn: !!data.mine, otherDevices: others.length });
  const church = site || "Fold";
  // The church turned notifications off (DEC-124): the card says so, with
  // nothing to press (PLAN M3.13 acceptance).
  if (state === "church_off") return <section aria-labelledby="push-card-heading" className="bg-surface rounded-lg border border-border p-6 space-y-4">
    <h2 id="push-card-heading" className="text-lg font-semibold">Notifications on this device</h2>
    <p className="text-text">{site === undefined ? "Your church" : church} hasn't turned on notifications on devices. You'll still get email and Alerts in Fold.</p>
  </section>;
  const mobile = data.platform === "ios" || data.platform === "android";

  // Each action shows the server's line as it is (DEC-129) or an error.
  async function act(fn: () => Promise<void>) {
    if (busy) return;
    setBusy(true); setStatus(""); setError(null);
    try {
      await fn();
    } catch (err) {
      if (alive.current) setError(errorText(err, "Something went wrong. Try again."));
    } finally {
      if (alive.current) setBusy(false);
    }
  }
  const refreshed = async (mine: Loaded["mine"]) => {
    const s = await SettingsApi.get();
    if (alive.current) setData((d) => d && { ...d, mine, setUpBefore: false, push: s.settings.push ?? NO_PUSH });
    heading.current?.focus();
  };

  function turnOn() {
    if (!data || busy) return;
    const { publicKey } = data;
    // Ask right away, inside the tap: Safari only asks from a gesture.
    const asked = Notification.requestPermission();
    void act(async () => {
      const permission = await asked;
      if (permission === "denied") {
        if (alive.current) setData((d) => d && { ...d, device: "blocked" });
        heading.current?.focus();
        return;
      }
      if (permission !== "granted") {
        setStatus("Notifications are still off. You can turn them on any time.");
        return;
      }
      let sub: PushSubscription;
      try {
        sub = await subscribeThisDevice(publicKey);
      } catch {
        setError("This browser couldn't turn on notifications. Try again, or use email on this device.");
        return;
      }
      const r = await Push.save(sub.toJSON());
      if (!r.saved || !r.device) {
        setStatus(r.message || "Notifications are still off. Try again in a few minutes.");
        return;
      }
      resaved.set(sub.endpoint, Promise.resolve(r));
      await refreshed({ id: r.device.id, sub });
      setStatus("Turned on. Send a test to check it works.");
    });
  }

  function sendTest() {
    const mine = data?.mine;
    if (!mine) return;
    void act(async () => {
      const r = await Push.test(mine.sub.endpoint);
      setStatus(r.sent ? "Sent. It should show on this device in a moment." : r.message || "The test didn't go through. Try again in a few minutes.");
    });
  }

  function turnOffHere() {
    const mine = data?.mine;
    if (!mine) return;
    void act(async () => {
      await Push.remove(mine.sub.endpoint);
      resaved.delete(mine.sub.endpoint);
      await mine.sub.unsubscribe().catch(() => false);
      await refreshed(null);
      setStatus("Notifications are off for this device.");
    });
  }

  function turnOffEverywhere() {
    if (!window.confirm("Turn off notifications on all your devices? You can turn them on again on each device.")) return;
    void act(async () => {
      const r = await Push.turnOffEverywhere();
      resaved.clear();
      await data?.mine?.sub.unsubscribe().catch(() => false);
      if (alive.current) setData((d) => d && { ...d, mine: null, setUpBefore: false, push: r.push });
      heading.current?.focus();
      setStatus("Notifications are off on all your devices.");
    });
  }

  function changeKind(category: EmailKind["category"], off: boolean) {
    void act(async () => {
      const r = await SettingsApi.updatePush(category, off);
      if (alive.current) setData((d) => d && { ...d, push: r.settings.push ?? d.push });
      setStatus("Saved");
    });
  }

  const asks = mobile ? "Your phone will ask once. You can change your mind any time." : "Your browser will ask once. You can change your mind any time.";
  const showKinds = data.push.on && (state === "on" || others.length > 0) && data.push.kinds.length > 0;

  return <section aria-labelledby="push-card-heading" className="bg-surface rounded-lg border border-border p-6 space-y-4">
    <h2 id="push-card-heading" ref={heading} tabIndex={-1} className="text-lg font-semibold">Notifications on this device</h2>
    {error && <FormError>{error}</FormError>}

    {(state === "unavailable" || state === "unavailable_ios") && <p className="text-text">
      {state === "unavailable_ios"
        ? "Notifications on an iPhone or iPad need iOS 16.4 or newer. Until you update, use email on this device."
        : "Notifications need a secure (https) connection and a browser that can receive them. On this device, use email."}
    </p>}

    {state === "install_first" && <>
      <p className="text-text">To get notifications on your iPhone or iPad, add {church} to your Home Screen first.</p>
      <ol className="list-decimal space-y-1 pl-5 text-text">
        <li>In Safari, tap the Share button: the square with an arrow pointing up.</li>
        <li>Tap Add to Home Screen, then tap Add.</li>
        <li>Open {church} from your Home Screen, sign in, and come back to Me → Notifications.</li>
      </ol>
      <ButtonLink variant="secondary" to={SETUP}>See the steps</ButtonLink>
    </>}

    {state === "blocked" && <>
      <p className="text-text">This device is blocking notifications from {church}. To allow them again:</p>
      <ol className="list-decimal space-y-1 pl-5 text-text">
        {undoSteps(data.platform, church).map((s) => <li key={s}>{s}</li>)}
      </ol>
      <ButtonLink variant="link" to={SETUP}>More help with notifications</ButtonLink>
    </>}

    {(state === "ready" || state === "on_elsewhere") && <>
      {state === "on_elsewhere" && <p className="text-text">Notifications are on for your other devices, not this one.</p>}
      {data.setUpBefore && <p className="text-text">This device was set up before. Turn it on again.</p>}
      <div className="space-y-2">
        <Button variant="primary" onClick={turnOn} aria-disabled={busy || undefined}>
          {state === "on_elsewhere" ? "Turn on notifications on this device" : "Turn on notifications"}
        </Button>
        <p className="text-sm text-text-muted">{asks}</p>
      </div>
    </>}

    {state === "on" && <>
      <p className="text-text">Notifications are on for this device.</p>
      <div className="flex flex-col gap-3 sm:flex-row sm:flex-wrap">
        <Button variant="primary" onClick={sendTest} aria-disabled={busy || undefined}>Send a test notification</Button>
        <Button variant="secondary" onClick={turnOffHere} aria-disabled={busy || undefined}>Turn off notifications on this device</Button>
      </div>
    </>}

    <p role="status" className="text-sm text-text-muted">{status}</p>

    {(state === "on" || state === "on_elsewhere") && others.length > 0 && <OtherDevices devices={others} />}

    {showKinds && <>
      <Choices legend="What comes to your devices" hint="Turn off the kinds you don't want. Everything still shows in Alerts.">
        {data.push.kinds.map((k) => {
          const copy = EMAIL_KIND_COPY[k.category];
          return <Checkbox key={k.category} label={copy.label} description={copy.hint} checked={!k.off} aria-disabled={busy || undefined}
            onChange={(e) => { if (!busy) changeKind(k.category, !e.target.checked); }} />;
        })}
      </Choices>
      <p className="text-sm text-text-muted">Account and safety notices always come to a device with notifications on.</p>
    </>}

    {(state === "on" || state === "on_elsewhere") && <Button variant="ghost" onClick={turnOffEverywhere} aria-disabled={busy || undefined}>Turn off notifications everywhere</Button>}

    <p className="text-sm text-text-muted">Notifications are a shortcut. Everything still shows in Alerts in Fold, and in your email.</p>
  </section>;
}

function OtherDevices({ devices }: { devices: PushDevice[] }) {
  return <div>
    <h3 className="label">Other devices: {devices.length}</h3>
    <ul className="space-y-1 text-text">
      {devices.map((d) => <li key={d.id}>{d.label} <span className="text-sm text-text-muted">· added {formatShortDay(d.createdAt)}</span></li>)}
    </ul>
  </div>;
}

// undoSteps: how to allow notifications again on each platform (research
// 2.7, "Blocked it by mistake?"). The directions page has the long form.
function undoSteps(platform: Platform, church: string): string[] {
  const host = window.location.host;
  switch (platform) {
    case "ios":
      return [
        `Open Settings → Notifications → ${church} and turn Allow Notifications on.`,
        `If ${church} isn't listed, remove the icon from your Home Screen and add it again from Safari.`,
      ];
    case "android":
      return [
        "Tap the lock icon next to the address in Chrome, then Permissions, and set Notifications to Allow.",
        "Then come back here and tap Turn on notifications again.",
      ];
    case "mac-safari":
      return [
        `Choose Safari → Settings → Websites → Notifications, and set ${host} to Allow.`,
        "Then reload this page and click Turn on notifications again.",
      ];
    default:
      return [
        "Click the icon left of the address bar, find Notifications, and set it to Allow.",
        "Then reload this page and click Turn on notifications again.",
      ];
  }
}
