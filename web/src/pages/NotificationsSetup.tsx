import { useEffect, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { Site } from "../api";
import { useAuth } from "../auth";
import { deviceState, localSubscription, readPushEnv, type DeviceState, type Platform } from "../lib/pushState";
import { Skeleton } from "../components/ui/Skeleton";
import { NotFound } from "./NotFound";

// /notifications/setup (M3.13c2, research 2.7, DEC-127): how to get Fold's
// notifications on each kind of device, and how to undo it. Public, so a
// member can open it from a link before signing in. This device's section
// comes first; the others follow under "Using a different device?". When
// the church turned notifications off it isn't there at all (DEC-124): the
// page is the ordinary "Page not found".

interface Section {
  platform: Platform;
  heading: string;
  body: ReactNode;
  // "Blocked it by mistake?": also shown at the top when this device blocks.
  blocked: ReactNode;
}

const MeNotifications = () => <Link to="/profile/notifications" className="font-semibold text-primary underline">Me → Notifications</Link>;

function sections(church: string, site: string): Section[] {
  const b = (s: string) => <strong>{s}</strong>;
  return [
    {
      platform: "ios",
      heading: "Get notifications on your iPhone or iPad",
      body: <>
        <p>Notifications work on iPhones and iPads running iOS 16.4 or newer, once {church} is on your Home Screen. It takes about a minute.</p>
        <ol className="list-decimal space-y-2 pl-5">
          <li>Open {b(site)} in {b("Safari")}. (If you're reading this in Safari already, you're set.)</li>
          <li>Tap the {b("Share")} button: the square with an arrow pointing up, at the bottom of the screen on an iPhone, top right on an iPad.</li>
          <li>Scroll down and tap {b("Add to Home Screen")}, then tap {b("Add")}.</li>
          <li>Close Safari and open {b(church)} from your Home Screen. It opens like an app.</li>
          <li>Sign in once. (The Home Screen app keeps its own sign-in.)</li>
          <li>Go to <MeNotifications /> and tap {b("Turn on notifications")}. When your iPhone asks, tap {b("Allow")}.</li>
        </ol>
        <p>That's it. Notifications show on your Lock Screen and in Notification Center like any app's. If you'd rather keep them private on a locked phone, open {b("Settings → Notifications → Show Previews")} and choose {b("When Unlocked")}.</p>
        <details>
          <summary className="cursor-pointer py-3 font-semibold">Using a different browser?</summary>
          <p>Chrome, Firefox and Edge on your iPhone can add {church} to your Home Screen too (the same Share button, then {b("Add to Home Screen")}) if you're on iOS 16.4 or newer. If you don't see that option, open the link in Safari and follow the steps above.</p>
        </details>
        <p>{b("To stop notifications:")} on your iPhone, open {b("Settings → Notifications")}, find {b(church)}, and turn {b("Allow Notifications")} off. Or in Fold, <MeNotifications /> → {b("Turn off notifications on this device")}. Removing the icon from your Home Screen stops them too.</p>
      </>,
      blocked: <>
        <p>iOS asks only once. Open {b(`Settings → Notifications → ${church}`)} and turn {b("Allow Notifications")} on.</p>
        <p>If {church} isn't listed, remove the icon from your Home Screen and add it again from Safari.</p>
      </>,
    },
    {
      platform: "android",
      heading: "Get notifications on your Android phone",
      body: <>
        <ol className="list-decimal space-y-2 pl-5">
          <li>Open {b(site)} in {b("Chrome")} and sign in.</li>
          <li>Go to <MeNotifications /> and tap {b("Turn on notifications")}.</li>
          <li>When Chrome asks, tap {b("Allow")}.</li>
        </ol>
        <p>You can also add {church} to your home screen: tap the {b("⋮")} menu, then {b("Add to Home screen")} (or {b("Install app")}). Notifications work either way.</p>
        <p>{b("To stop notifications:")} in Fold, <MeNotifications /> → {b("Turn off notifications on this device")}. Or in Chrome, tap the lock icon next to the address, then {b("Permissions → Notifications")}, and turn it off. On your phone, {b("Settings → Apps → Chrome → Notifications")} lists each site too.</p>
      </>,
      blocked: <p>Tap the lock icon next to the address in Chrome, then {b("Permissions")}, and set {b("Notifications")} to Allow. Then come back and tap {b("Turn on notifications")} again.</p>,
    },
    {
      platform: "computer",
      heading: "Get notifications on your computer",
      body: <>
        <p>In Chrome, Edge or Firefox:</p>
        <ol className="list-decimal space-y-2 pl-5">
          <li>Open {b(site)} and sign in.</li>
          <li>Go to <MeNotifications /> and click {b("Turn on notifications")}.</li>
          <li>When your browser asks, choose {b("Allow")}.</li>
        </ol>
        <p>Notifications arrive while the browser is running, even with {church} closed.</p>
        <p>{b("To stop notifications:")} <MeNotifications /> → {b("Turn off notifications on this device")}, or click the icon left of the address bar (a lock or a tune icon) and set {b("Notifications")} to Block.</p>
      </>,
      blocked: <p>Click the icon left of the address bar, find {b("Notifications")}, and set it to Allow. Then reload and click {b("Turn on notifications")} again.</p>,
    },
    {
      platform: "mac-safari",
      heading: "Get notifications in Safari on your Mac",
      body: <>
        <p>Safari 16.1 or newer (macOS Ventura and later).</p>
        <ol className="list-decimal space-y-2 pl-5">
          <li>Open {b(site)} in Safari and sign in.</li>
          <li>Go to <MeNotifications /> and click {b("Turn on notifications")}.</li>
          <li>When Safari asks, choose {b("Allow")}.</li>
        </ol>
        <p>{b("To stop notifications:")} <MeNotifications /> → {b("Turn off notifications on this device")}, or in Safari choose {b("Safari → Settings → Websites → Notifications")} and set {site} to Deny. Your Mac's {b("System Settings → Notifications")} lists {church} too.</p>
      </>,
      blocked: <p>Choose {b("Safari → Settings → Websites → Notifications")}, set {site} to Allow, reload, and click {b("Turn on notifications")} again.</p>,
    },
  ];
}

// What this browser says about itself, for the line at the top.
type Here = { platform: Platform; device: DeviceState; on: boolean };

export function NotificationsSetupPage() {
  const { user } = useAuth();
  const [site, setSite] = useState<{ name: string; pushEnabled: boolean } | null>(null);
  const [here, setHere] = useState<Here | null>(null);

  useEffect(() => {
    let alive = true;
    // A failed read shows the directions anyway: they are public words.
    Site.get().then(
      (s) => { if (alive) setSite({ name: s.name, pushEnabled: s.pushEnabled !== false }); },
      () => { if (alive) setSite({ name: "", pushEnabled: true }); },
    );
    const env = readPushEnv();
    const device = deviceState(env);
    const known = { platform: env.platform, device, on: false };
    if (device === "available" && env.permission === "granted") {
      localSubscription().then((sub) => { if (alive) setHere({ ...known, on: !!sub }); });
    } else {
      setHere(known);
    }
    return () => { alive = false; };
  }, []);

  if (!site || !here) return <div className="max-w-2xl mx-auto px-4 py-8"><Skeleton kind="page" label="Loading the directions…" /></div>;
  // The church turned notifications off (DEC-124): the page isn't there.
  if (!site.pushEnabled) return <NotFound />;

  const church = site.name || "Fold";
  const all = sections(church, window.location.host);
  const mine = all.find((s) => s.platform === here.platform) ?? all[2];
  const others = all.filter((s) => s !== mine);

  return <div className="max-w-2xl mx-auto px-4 py-8 space-y-8 wrap-anywhere">
    <div className="space-y-3">
      <h1 className="text-3xl font-bold">Get notifications on your phone</h1>
      <p className="text-text-muted">{church} can send notifications to your phone or computer, like any app: a cancelled date, a new group, a message from your leader.</p>
      <Here here={here} section={mine} church={church} signedIn={!!user} />
    </div>

    <section aria-labelledby="setup-mine" className="space-y-4 rounded-lg border border-border bg-surface p-6">
      <h2 id="setup-mine" className="text-xl font-semibold">{mine.heading}</h2>
      {mine.body}
      <div className="space-y-2">
        <h3 className="font-semibold">Blocked it by mistake?</h3>
        {mine.blocked}
      </div>
      <p className="text-sm text-text-muted">Notifications are a shortcut. Everything still shows in Alerts in {church}, and in your email.</p>
    </section>

    <section aria-labelledby="setup-others" className="space-y-6">
      <h2 id="setup-others" className="text-xl font-semibold">Using a different device?</h2>
      {others.map((s) => <section key={s.platform} aria-labelledby={`setup-${s.platform}`} className="space-y-4 rounded-lg border border-border bg-surface p-6">
        <h3 id={`setup-${s.platform}`} className="text-lg font-semibold">{s.heading}</h3>
        {s.body}
        <div className="space-y-2">
          <h4 className="font-semibold">Blocked it by mistake?</h4>
          {s.blocked}
        </div>
      </section>)}
    </section>
  </div>;
}

// Here is the line about this browser: notifications already on, blocked
// (with this device's undo steps), or not possible here.
function Here({ here, section, church, signedIn }: { here: Here; section: Section; church: string; signedIn: boolean }) {
  const box = "space-y-2 rounded-lg border border-border bg-sunken p-4 text-text";
  if (here.on) {
    return <div className={box}>
      <p className="font-semibold">Notifications are already on in this browser.</p>
      <p>{signedIn ? <>To send a test or turn them off, go to <MeNotifications />.</> : <>Sign in and go to <MeNotifications /> to send a test or turn them off.</>}</p>
    </div>;
  }
  if (here.device === "blocked") {
    return <div className={box}>
      <p className="font-semibold">This device is blocking notifications from {church}. To allow them again:</p>
      {section.blocked}
    </div>;
  }
  if (here.device === "unavailable_ios") {
    return <div className={box}><p>Notifications on an iPhone or iPad need iOS 16.4 or newer. Until you update, use email on this device.</p></div>;
  }
  if (here.device === "unavailable") {
    return <div className={box}><p>This browser can't receive notifications: they need a secure (https) connection and a browser that can. Try another browser below, or use email.</p></div>;
  }
  return null;
}
