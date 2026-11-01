// The "Notifications on this device" card's side of web push (M3.13c1,
// research 2.3 and 2.7): what this browser can do, the card's state, and the
// calls that turn its subscription on. The page side never registers the
// service worker on load: only the card's "Turn on notifications" does.
import { isStandalone, registration } from "./push";

export type Platform = "ios" | "android" | "mac-safari" | "computer";

// The facts the card's state comes from, read from the browser.
export interface PushEnv {
  secure: boolean;
  // 'serviceWorker' in navigator, 'PushManager' in window, and Notification.
  supported: boolean;
  permission: NotificationPermission | null;
  standalone: boolean;
  platform: Platform;
  // iOS or iPadOS major and minor, when the user agent says.
  iosVersion: [number, number] | null;
}

// What the device allows before asking the server anything.
export type DeviceState = "unavailable" | "unavailable_ios" | "install_first" | "blocked" | "available";

// platformOf reads the platform from the user agent. iPadOS asks for
// desktop pages as a Mac, which only touch points give away.
export function platformOf(ua: string, maxTouchPoints = 0): Platform {
  if (/iPhone|iPad|iPod/.test(ua) || (/Macintosh/.test(ua) && maxTouchPoints > 1)) return "ios";
  if (/Android/.test(ua)) return "android";
  if (/Macintosh/.test(ua) && /Version\/[\d.]+.*Safari\//.test(ua) && !/Chrome|Chromium|Edg\/|Firefox/.test(ua)) return "mac-safari";
  return "computer";
}

// iosVersion: "CPU iPhone OS 16_3 like Mac OS X", or Safari's own
// "Version/17.1" when iPadOS passes as a Mac.
export function iosVersion(ua: string): [number, number] | null {
  const m = ua.match(/ OS (\d+)_(\d+)/) ?? ua.match(/Version\/(\d+)\.(\d+)/);
  return m ? [Number(m[1]), Number(m[2])] : null;
}

export function readPushEnv(): PushEnv {
  const supported = "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
  return {
    secure: window.isSecureContext === true,
    supported,
    permission: "Notification" in window ? Notification.permission : null,
    standalone: isStandalone(),
    platform: platformOf(navigator.userAgent, navigator.maxTouchPoints ?? 0),
    iosVersion: iosVersion(navigator.userAgent),
  };
}

// deviceState: iOS before 16.4 can't, even from the Home Screen; newer iOS
// can only from the Home Screen app (Safari tabs have no PushManager).
export function deviceState(env: PushEnv): DeviceState {
  if (!env.secure) return "unavailable";
  if (env.platform === "ios") {
    const v = env.iosVersion;
    if (v && (v[0] < 16 || (v[0] === 16 && v[1] < 4))) return "unavailable_ios";
    if (!env.standalone) return "install_first";
  }
  if (!env.supported) return "unavailable";
  if (env.permission === "denied") return "blocked";
  return "available";
}

// The card's states (research 2.7). church_off is a one-line card (DEC-124);
// signed_out_app is the sign-in page's line, not the card.
export type PushCardState = "unavailable" | "unavailable_ios" | "install_first" | "blocked" | "ready" | "on" | "on_elsewhere" | "church_off";

export function cardState(f: { enabled: boolean; device: DeviceState; thisDeviceOn: boolean; otherDevices: number }): PushCardState {
  if (!f.enabled) return "church_off";
  if (f.device !== "available") return f.device;
  if (f.thisDeviceOn) return "on";
  return f.otherDevices > 0 ? "on_elsewhere" : "ready";
}

// keyBytes: the VAPID public key (base64url) as applicationServerKey bytes.
// The worker has its own copy (sw/logic.ts) so the two bundles share nothing.
function keyBytes(base64url: string): Uint8Array<ArrayBuffer> {
  const pad = "=".repeat((4 - (base64url.length % 4)) % 4);
  const raw = atob((base64url + pad).replace(/-/g, "+").replace(/_/g, "/"));
  const out = new Uint8Array(new ArrayBuffer(raw.length));
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

// sameKey: the subscription was made with the church's current key. One
// made before a key rotation can't receive anything now.
export function sameKey(sub: PushSubscription, publicKey: string): boolean {
  const have = sub.options?.applicationServerKey;
  if (!have) return true; // the browser doesn't say; trust it
  const a = new Uint8Array(have);
  const b = keyBytes(publicKey);
  return a.length === b.length && a.every((x, i) => x === b[i]);
}

// localSubscription is this browser's push subscription, if it has one.
export async function localSubscription(): Promise<PushSubscription | null> {
  const reg = await registration();
  if (!reg?.pushManager) return null;
  try {
    return await reg.pushManager.getSubscription();
  } catch {
    return null;
  }
}

// subscribeThisDevice registers /sw.js (scope /) and subscribes with the
// church's key, replacing a subscription made with an older key. Call it
// after the person tapped and the permission is granted.
export async function subscribeThisDevice(publicKey: string): Promise<PushSubscription> {
  await navigator.serviceWorker.register("/sw.js", { scope: "/" });
  const reg = await navigator.serviceWorker.ready;
  const old = await reg.pushManager.getSubscription();
  if (old && sameKey(old, publicKey)) return old;
  if (old) await old.unsubscribe().catch(() => false);
  return reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: keyBytes(publicKey) });
}

