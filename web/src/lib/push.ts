// Notifications on this device (M3.13c1, research 2.3): the small part
// every page needs (logout, the icon badge, the Home Screen sign-in line).
// The card's detection and turn-on calls are in pushState.ts, which only
// Me → Notifications loads. Nothing here registers the service worker:
// only the card's "Turn on notifications" does.

// isStandalone: running as a Home Screen or installed app.
export function isStandalone(): boolean {
  const media = typeof window.matchMedia === "function" && window.matchMedia("(display-mode: standalone)").matches;
  return media || (navigator as Navigator & { standalone?: boolean }).standalone === true;
}

// registration is the worker already registered for this site, if any,
// without registering one. A browser that never answers counts as none.
export async function registration(): Promise<ServiceWorkerRegistration | null> {
  if (!("serviceWorker" in navigator)) return null;
  try {
    const found = await Promise.race([
      navigator.serviceWorker.getRegistration("/"),
      new Promise<undefined>((resolve) => setTimeout(() => resolve(undefined), 2000)),
    ]);
    return found ?? null;
  } catch {
    return null;
  }
}

type Badging = Navigator & { setAppBadge?: (n?: number) => Promise<void>; clearAppBadge?: () => Promise<void> };

// syncAppBadge puts the bell's count on the app icon, where the browser
// has the Badging API, and clears it at 0.
export function syncAppBadge(count: number) {
  const nav = navigator as Badging;
  try {
    const p = count > 0 ? nav.setAppBadge?.(count) : nav.clearAppBadge?.();
    p?.catch(() => {});
  } catch {
    // Not an installed app, or no permission: nothing to show.
  }
}

// deviceForLogout finds this browser's subscription before signing out;
// the logout request carries its endpoint so the server deletes the device
// in the same request.
export async function deviceForLogout(): Promise<{ reg: ServiceWorkerRegistration; sub: PushSubscription | null } | null> {
  const reg = await registration();
  if (!reg) return null;
  let sub: PushSubscription | null = null;
  try {
    sub = (await reg.pushManager?.getSubscription()) ?? null;
  } catch {
    sub = null;
  }
  return { reg, sub };
}

// forgetDevice runs after logout: the browser drops its subscription, the
// notifications on screen close, and the icon's count clears, so the next
// person on a shared device sees nothing meant for this one.
export async function forgetDevice(device: { reg: ServiceWorkerRegistration; sub: PushSubscription | null } | null) {
  syncAppBadge(0);
  if (!device) return;
  await device.sub?.unsubscribe().catch(() => false);
  try {
    const shown = await device.reg.getNotifications();
    shown.forEach((n) => n.close());
  } catch {
    // Nothing shown, or the browser won't say.
  }
}
