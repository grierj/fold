// Fold's service worker (M3.13c1, research 2.3), built to /sw.js and
// registered only by "Turn on notifications". It handles three events:
// push, notificationclick and pushsubscriptionchange. There is no fetch
// handler and no cache, on purpose: the worker can never serve a stale
// page after logout or fight the shell's no-store (10.1). Messages from
// pages are ignored. The decisions live in sw/logic.ts.
import { fromPush, openOrFocus, resubscribe, sameOrigin, type WindowLike } from "./sw/logic";

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
interface WorkerScope {
  location: { origin: string };
  registration: ServiceWorkerRegistration;
  navigator: { setAppBadge?: (n?: number) => Promise<void>; clearAppBadge?: () => Promise<void> };
  clients: {
    matchAll(o: { type: "window"; includeUncontrolled: boolean }): Promise<WindowLike[]>;
    openWindow(url: string): Promise<unknown>;
  };
  addEventListener(type: "push", fn: (e: PushEventLike) => void): void;
  addEventListener(type: "notificationclick", fn: (e: ClickEventLike) => void): void;
  addEventListener(type: "pushsubscriptionchange", fn: (e: SubscriptionChangeLike) => void): void;
}

const sw = globalThis as unknown as WorkerScope;
const origin = sw.location.origin;

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
      fetch: (input, init) => fetch(input, init),
      subscribe: (key) => sw.registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key }),
    }, event.newSubscription ?? null).catch(() => false),
  );
});
