// The service worker's event wiring (M3.13c1, research 2.3), apart from the
// entry (src/sw.ts) so a test can hand it a fake worker scope. Five events:
// install and activate (a new /sw.js takes over at once and controls open
// tabs, so a tapped notification can reuse one), push, notificationclick and
// pushsubscriptionchange. There is no fetch handler and no cache, on purpose:
// the worker can never serve a stale page after logout or fight the shell's
// no-store (10.1). Messages from pages are ignored. The decisions live in
// logic.ts.
import { fromPush, openOrFocus, resubscribe, sameOrigin, type WindowLike } from "./logic";

// The project's types are the page's (lib DOM), so the worker's own global
// is described here rather than mixing in lib WebWorker.
interface WaitEvent extends Event {
  waitUntil(p: Promise<unknown>): void;
}
interface PushEventLike extends WaitEvent {
  data: { text(): string } | null;
}
interface ClickEventLike extends WaitEvent {
  notification: Notification;
}
interface SubscriptionChangeLike extends WaitEvent {
  newSubscription?: PushSubscription | null;
}
export interface WorkerScope {
  location: { origin: string };
  registration: ServiceWorkerRegistration;
  navigator: { setAppBadge?: (n?: number) => Promise<void>; clearAppBadge?: () => Promise<void> };
  clients: {
    matchAll(o: { type: "window"; includeUncontrolled: boolean }): Promise<WindowLike[]>;
    openWindow(url: string): Promise<unknown>;
    claim(): Promise<void>;
  };
  skipWaiting(): Promise<void>;
  fetch: typeof fetch;
  addEventListener(type: "install" | "activate", fn: (e: WaitEvent) => void): void;
  addEventListener(type: "push", fn: (e: PushEventLike) => void): void;
  addEventListener(type: "notificationclick", fn: (e: ClickEventLike) => void): void;
  addEventListener(type: "pushsubscriptionchange", fn: (e: SubscriptionChangeLike) => void): void;
}

export function wire(sw: WorkerScope) {
  const origin = sw.location.origin;

  // A new worker replaces the old one at once instead of waiting for every
  // Fold tab to close; it has no cache to migrate.
  sw.addEventListener("install", (event) => {
    event.waitUntil(sw.skipWaiting());
  });
  // And it controls tabs opened before it, so notificationclick can
  // navigate an open tab rather than open a new window.
  sw.addEventListener("activate", (event) => {
    event.waitUntil(sw.clients.claim());
  });

  sw.addEventListener("push", (event) => {
    let text: string | null = null;
    try {
      text = event.data ? event.data.text() : null;
    } catch {
      text = null;
    }
    const shown = fromPush(text, origin);
    const badge = shown.appBadge === undefined ? Promise.resolve()
      : (shown.appBadge > 0 ? sw.navigator.setAppBadge?.(shown.appBadge) : sw.navigator.clearAppBadge?.())?.catch(() => {});
    event.waitUntil(Promise.all([sw.registration.showNotification(shown.title, shown.options), badge]));
  });

  sw.addEventListener("notificationclick", (event) => {
    event.notification.close();
    const data = event.notification.data as { navigate?: unknown } | null;
    const target = sameOrigin(data?.navigate, origin);
    event.waitUntil(
      sw.clients.matchAll({ type: "window", includeUncontrolled: true })
        .then((windows) => openOrFocus(target, origin, windows, (url) => sw.clients.openWindow(url))),
    );
  });

  sw.addEventListener("pushsubscriptionchange", (event) => {
    event.waitUntil(
      resubscribe({
        fetch: (input, init) => sw.fetch(input, init),
        subscribe: (key) => sw.registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key }),
      }, event.newSubscription ?? null).catch(() => false),
    );
  });
}
