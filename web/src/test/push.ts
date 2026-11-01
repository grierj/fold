import { vi } from "vitest";

// A stand-in for the browser's push APIs in jsdom, which has none:
// Notification, PushManager, a service worker registration, the secure
// context, the user agent and display-mode. install() returns the fakes;
// uninstall() puts jsdom back.

export interface FakeSub {
  endpoint: string;
  options: { applicationServerKey: ArrayBuffer | null };
  toJSON: () => PushSubscriptionJSON;
  unsubscribe: ReturnType<typeof vi.fn>;
}

let n = 0;
export function fakeSub(endpoint = `https://fcm.googleapis.com/fcm/send/unit-${++n}`, key: number[] | null = null): FakeSub {
  return {
    endpoint,
    options: { applicationServerKey: key && new Uint8Array(key).buffer },
    toJSON: () => ({ endpoint, expirationTime: null, keys: { p256dh: "BCVx", auth: "BTBZ" } }),
    unsubscribe: vi.fn(async () => true),
  };
}

export interface BrowserOptions {
  secure?: boolean;
  supported?: boolean;
  permission?: NotificationPermission;
  // What requestPermission answers after the tap.
  answer?: NotificationPermission;
  standalone?: boolean;
  userAgent?: string;
  maxTouchPoints?: number;
  // This browser already has a registration with this subscription.
  subscription?: FakeSub | null;
  registered?: boolean;
}

const touched: { target: object; key: string; had: boolean; value: PropertyDescriptor | undefined }[] = [];
function set(target: object, key: string, value: unknown) {
  touched.push({ target, key, had: Object.prototype.hasOwnProperty.call(target, key), value: Object.getOwnPropertyDescriptor(target, key) });
  Object.defineProperty(target, key, { value, configurable: true, writable: true });
}

export function installBrowser(o: BrowserOptions = {}) {
  let current: FakeSub | null = o.subscription ?? null;
  let registered = o.registered ?? !!o.subscription;
  const shown = [{ close: vi.fn() }, { close: vi.fn() }];
  const pushManager = {
    getSubscription: vi.fn(async () => current),
    subscribe: vi.fn(async () => (current = fakeSub())),
  };
  const registration = { pushManager, getNotifications: vi.fn(async () => shown) };
  const serviceWorker = {
    getRegistration: vi.fn(async () => (registered ? registration : undefined)),
    register: vi.fn(async () => { registered = true; return registration; }),
    get ready() { return Promise.resolve(registration); },
  };
  const permission = { value: o.permission ?? "default" };
  const Notification = {
    get permission() { return permission.value; },
    requestPermission: vi.fn(async () => (permission.value = o.answer ?? "granted")),
  };
  set(window, "isSecureContext", o.secure ?? true);
  set(window, "matchMedia", (q: string) => ({ matches: q.includes("standalone") && !!o.standalone, media: q }));
  set(navigator, "userAgent", o.userAgent ?? "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/126.0 Safari/537.36");
  set(navigator, "maxTouchPoints", o.maxTouchPoints ?? 0);
  if (o.supported ?? true) {
    set(window, "PushManager", function PushManager() {});
    set(window, "Notification", Notification);
    set(navigator, "serviceWorker", serviceWorker);
  }
  return { pushManager, registration, serviceWorker, Notification, shown, current: () => current };
}

export function uninstallBrowser() {
  while (touched.length) {
    const t = touched.pop()!;
    if (t.had && t.value) Object.defineProperty(t.target, t.key, t.value);
    else delete (t.target as Record<string, unknown>)[t.key];
  }
}
