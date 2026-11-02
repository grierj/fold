// The service worker's decisions, kept apart from its event wiring
// (src/sw.ts) so they can be tested without a worker. Only the worker
// imports this file, so nothing here lands in the page bundle.

// What the push handler shows: the arguments to showNotification, and the
// icon count to set when the payload carries one.
export interface Shown {
  title: string;
  options: NotificationOptions & { data: { navigate: string } };
  appBadge?: number;
}

// The icon a notification shows beside its text (the manifest's icon).
const ICON = "/assets/brand/fold-icon-192.png";
// The monochrome badge shown where the browser supports one.
const BADGE = "/assets/brand/fold-badge-96.png";

// FALLBACK is what shows when a push can't be read. Something must show:
// Safari revokes a subscription that receives a silent push.
export function fallback(origin: string): Shown {
  return {
    title: "Open Fold",
    options: { body: "You have an update.", icon: ICON, badge: BADGE, data: { navigate: origin + "/" } },
  };
}

// sameOrigin makes a link absolute and keeps it only if it is on the
// worker's own origin; anything else becomes the home page, so a payload
// can never send a tap somewhere else.
export function sameOrigin(link: unknown, origin: string): string {
  if (typeof link !== "string" || link === "") return origin + "/";
  try {
    const url = new URL(link, origin + "/");
    if (url.origin === origin && (url.protocol === "https:" || url.protocol === "http:")) return url.href;
  } catch {
    // Not a URL: the home page.
  }
  return origin + "/";
}

// fromPush reads a Declarative Web Push message ({"web_push": 8030,
// "notification": {...}}, internal/push Payload) and falls back when it
// isn't one.
export function fromPush(text: string | null | undefined, origin: string): Shown {
  let msg: unknown;
  try {
    msg = text ? JSON.parse(text) : null;
  } catch {
    return fallback(origin);
  }
  const n = (msg as { notification?: Record<string, unknown> } | null)?.notification;
  if (!n || typeof n !== "object" || typeof n.title !== "string" || n.title.trim() === "") return fallback(origin);
  const options: Shown["options"] = { icon: ICON, badge: BADGE, data: { navigate: sameOrigin(n.navigate, origin) } };
  if (typeof n.body === "string" && n.body) options.body = n.body;
  if (typeof n.tag === "string" && n.tag) options.tag = n.tag;
  const shown: Shown = { title: n.title, options };
  const badge = n.app_badge;
  if (typeof badge === "number" && Number.isInteger(badge) && badge >= 0) shown.appBadge = badge;
  return shown;
}

// clickPath is where a tap goes: the same-origin link rebuilt as origin,
// path, search and hash (any userinfo dropped). It stays absolute: a bare
// path like "//evil.com" (from "/.//evil.com") would be read as
// protocol-relative by openWindow and navigate and leave Fold.
export function clickPath(link: unknown, origin: string): string {
  const url = new URL(sameOrigin(link, origin));
  return url.origin + url.pathname + url.search + url.hash;
}

// The part of a window client the click handler uses.
export interface WindowLike {
  url: string;
  focus(): Promise<unknown>;
  navigate(url: string): Promise<unknown>;
}

// openOrFocus is the notification tap: focus an open Fold window and take
// it to the link, else open a new one. navigate() works only on windows
// this worker controls (a tab opened before notifications were turned on
// isn't one until it reloads), so a refusal opens a window instead.
export async function openOrFocus(
  target: string,
  origin: string,
  windows: readonly WindowLike[],
  openWindow: (url: string) => Promise<unknown>,
): Promise<void> {
  const mine = windows.find((w) => {
    try {
      return new URL(w.url).origin === origin;
    } catch {
      return false;
    }
  });
  if (mine) {
    try {
      await mine.focus();
      await mine.navigate(target);
      return;
    } catch {
      // Not controlled by this worker: open a window below.
    }
  }
  await openWindow(target);
}

// keyBytes turns the VAPID public key (base64url, from /push/config)
// into the applicationServerKey bytes.
export function keyBytes(base64url: string): Uint8Array<ArrayBuffer> {
  const pad = "=".repeat((4 - (base64url.length % 4)) % 4);
  const raw = atob((base64url + pad).replace(/-/g, "+").replace(/_/g, "/"));
  const out = new Uint8Array(new ArrayBuffer(raw.length));
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

// The parts of the worker's world resubscribe needs.
export interface ResubscribeDeps {
  fetch: typeof fetch;
  subscribe: (key: Uint8Array<ArrayBuffer>) => Promise<PushSubscription>;
}

// resubscribe answers pushsubscriptionchange: the browser replaced or
// dropped this device's subscription. With a session and the person's push
// consent still on, it asks for a CSRF token, reads the church's key,
// subscribes again (unless the browser already handed over the new
// subscription) and saves it, as the card's "Turn on notifications" does.
// After "Turn off notifications everywhere" it saves nothing: a save would
// opt the person back in. It returns whether the server saved it.
export async function resubscribe(deps: ResubscribeDeps, given: PushSubscription | null): Promise<boolean> {
  const csrfRes = await deps.fetch("/api/v1/auth/csrf", { credentials: "same-origin" });
  if (!csrfRes.ok) return false;
  const { csrfToken } = (await csrfRes.json()) as { csrfToken?: string };
  const settingsRes = await deps.fetch("/api/v1/me/notification-settings", { credentials: "same-origin" });
  if (!settingsRes.ok) return false; // signed out (401)
  const { settings } = (await settingsRes.json()) as { settings?: { push?: { on?: boolean } } };
  if (!settings?.push?.on) return false;
  const configRes = await deps.fetch("/api/v1/push/config", { credentials: "same-origin" });
  if (!configRes.ok || !csrfToken) return false; // signed out (401): nothing to save
  const config = (await configRes.json()) as { enabled?: boolean; publicKey?: string };
  if (!config.enabled || !config.publicKey) return false;
  const sub = given ?? (await deps.subscribe(keyBytes(config.publicKey)));
  const res = await deps.fetch("/api/v1/push/subscriptions", {
    method: "PUT",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", Accept: "application/json", "X-CSRF-Token": csrfToken },
    body: JSON.stringify(sub.toJSON()),
  });
  if (!res.ok) return false;
  return !!((await res.json()) as { saved?: boolean }).saved;
}
