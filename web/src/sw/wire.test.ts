import { expect, it, vi } from "vitest";
import { wire, type WorkerScope } from "./wire";

// A fake worker scope that records its listeners.
function scope() {
  const on: Record<string, (e: unknown) => void> = {};
  const sw = {
    location: { origin: "https://church.example" },
    registration: { showNotification: vi.fn(async () => {}), pushManager: { subscribe: vi.fn() } },
    navigator: {},
    clients: { matchAll: vi.fn(async () => []), openWindow: vi.fn(async () => null), claim: vi.fn(async () => {}) },
    skipWaiting: vi.fn(async () => {}),
    fetch: vi.fn(),
    addEventListener: (type: string, fn: (e: unknown) => void) => { on[type] = fn; },
  };
  wire(sw as unknown as WorkerScope);
  return { sw, on };
}
const event = (extra: object = {}) => {
  const waits: Promise<unknown>[] = [];
  return { waits, e: { waitUntil: (p: Promise<unknown>) => waits.push(p), ...extra } };
};

it("listens to five events and never to fetch or message", () => {
  const { on } = scope();
  expect(Object.keys(on).sort()).toEqual(["activate", "install", "notificationclick", "push", "pushsubscriptionchange"]);
});

it("takes over at once: install skips waiting, activate claims open tabs", async () => {
  const { sw, on } = scope();
  const install = event();
  on.install(install.e);
  await Promise.all(install.waits);
  expect(sw.skipWaiting).toHaveBeenCalled();
  const activate = event();
  on.activate(activate.e);
  await Promise.all(activate.waits);
  expect(sw.clients.claim).toHaveBeenCalled();
});

it("shows a push and opens its same-origin link when tapped", async () => {
  const { sw, on } = scope();
  const push = event({ data: { text: () => JSON.stringify({ web_push: 8030, notification: { title: "Hi", navigate: "https://church.example/groups/1" } }) } });
  on.push(push.e);
  await Promise.all(push.waits);
  expect(sw.registration.showNotification).toHaveBeenCalledWith("Hi", expect.objectContaining({ data: { navigate: "https://church.example/groups/1" } }));
  const close = vi.fn();
  const click = event({ notification: { close, data: { navigate: "https://evil.example/" } } });
  on.notificationclick(click.e);
  await Promise.all(click.waits);
  expect(close).toHaveBeenCalled();
  expect(sw.clients.openWindow).toHaveBeenCalledWith("/");
});
