import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { NotificationSettings, PushDevice, PushSettings } from "../types";
import { fakeSub, installBrowser, uninstallBrowser, type BrowserOptions } from "../test/push";

const api = vi.hoisted(() => ({
  config: vi.fn(),
  save: vi.fn(),
  remove: vi.fn(),
  turnOffEverywhere: vi.fn(),
  test: vi.fn(),
  get: vi.fn(),
  updatePush: vi.fn(),
}));
vi.mock("../api", async (orig) => ({
  ...(await orig<typeof import("../api")>()),
  Push: { config: api.config, save: api.save, remove: api.remove, turnOffEverywhere: api.turnOffEverywhere, test: api.test },
  NotificationSettings: { get: api.get, updatePush: api.updatePush },
  Site: { get: async () => ({ name: "Grace Church", timezone: "America/Boise", selfSignup: "request", roomsEnabled: false }) },
}));

import { PushDeviceCard } from "./PushDeviceCard";

const UA = {
  iphone: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
  oldIphone: "Mozilla/5.0 (iPhone; CPU iPhone OS 15_7 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/15.7 Mobile/15E148 Safari/604.1",
};

const device = (id: number, label: string): PushDevice => ({ id, label, createdAt: "2026-10-03T15:00:00Z", lastUsedAt: "2026-10-30T15:00:00Z" });
const kinds = (): PushSettings["kinds"] => [{ category: "groups", off: false }, { category: "events", off: false }];

// The server: push consent, its devices, and the switches.
let push: PushSettings;
const settings = (): NotificationSettings => ({ optionalEmailOff: false, kinds: [], push: structuredClone(push) });

beforeEach(() => {
  push = { on: false, devices: [], kinds: kinds() };
  Object.values(api).forEach((f) => f.mockReset());
  api.config.mockResolvedValue({ enabled: true, publicKey: "AQID" });
  api.get.mockImplementation(async () => ({ settings: settings() }));
  api.save.mockImplementation(async () => {
    const d = device(7, "Chrome on Windows");
    push = { ...push, on: true, devices: [d, ...push.devices.filter((x) => x.id !== 7)] };
    return { saved: true, device: d };
  });
  api.remove.mockImplementation(async () => {
    push = { ...push, devices: push.devices.filter((x) => x.id !== 7) };
    return {};
  });
  api.turnOffEverywhere.mockImplementation(async () => {
    push = { ...push, on: false, devices: [] };
    return { push: structuredClone(push) };
  });
  api.updatePush.mockImplementation(async (category: string, off: boolean) => {
    push = { ...push, kinds: push.kinds.map((k) => (k.category === category ? { ...k, off } : k)) };
    return { settings: settings() };
  });
});
afterEach(() => {
  uninstallBrowser();
  vi.restoreAllMocks();
});

function show(o: BrowserOptions = {}) {
  const browser = installBrowser(o);
  render(<MemoryRouter><PushDeviceCard load={async () => settings()} /></MemoryRouter>);
  return browser;
}
const card = () => screen.findByRole("region", { name: "Notifications on this device" });

describe("the eight states", () => {
  it("unavailable: no secure context, or no push in this browser", async () => {
    show({ secure: false });
    expect(await card()).toHaveTextContent("Notifications need a secure (https) connection and a browser that can receive them. On this device, use email.");
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    uninstallBrowser();
  });

  it("unavailable: a browser without PushManager", async () => {
    show({ supported: false });
    expect(await card()).toHaveTextContent("On this device, use email.");
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("unavailable: iOS older than 16.4, where the Home Screen can't help", async () => {
    show({ userAgent: UA.oldIphone, standalone: true });
    expect(await card()).toHaveTextContent("Notifications on an iPhone or iPad need iOS 16.4 or newer.");
    expect(screen.queryByRole("link", { name: "See the steps" })).not.toBeInTheDocument();
  });

  it("install first: iOS Safari in a tab points to the directions", async () => {
    show({ userAgent: UA.iphone, supported: false });
    const c = await card();
    expect(c).toHaveTextContent("add Grace Church to your Home Screen first");
    expect(within(c).getByRole("link", { name: "See the steps" })).toHaveAttribute("href", "/notifications/setup");
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("ready: offers Turn on notifications, and registers no worker on load", async () => {
    const b = show();
    expect(await screen.findByRole("button", { name: "Turn on notifications" })).toBeInTheDocument();
    expect(screen.getByText("Your browser will ask once. You can change your mind any time.")).toBeInTheDocument();
    expect(b.serviceWorker.register).not.toHaveBeenCalled();
    expect(b.Notification.requestPermission).not.toHaveBeenCalled();
    expect(api.save).not.toHaveBeenCalled();
  });

  it("on: the browser's subscription saves again, and the card offers the test and turning off", async () => {
    push = { on: true, devices: [device(7, "Chrome on Windows"), device(3, "iPhone · Home Screen app")], kinds: kinds() };
    const sub = fakeSub();
    show({ permission: "granted", subscription: sub });
    expect(await screen.findByText("Notifications are on for this device.")).toBeInTheDocument();
    expect(api.save).toHaveBeenCalledTimes(1);
    expect(api.save).toHaveBeenCalledWith(sub.toJSON());
    expect(screen.getByRole("button", { name: "Send a test notification" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Turn off notifications on this device" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Other devices: 1" })).toBeInTheDocument();
    expect(screen.getByText("iPhone · Home Screen app")).toBeInTheDocument();
    expect(screen.getByText(/added Oct 3/)).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "What comes to your devices" })).toBeInTheDocument();
  });

  it("on elsewhere: other devices have notifications on, this one doesn't", async () => {
    push = { on: true, devices: [device(3, "iPhone · Home Screen app"), device(4, "Chrome on Android")], kinds: kinds() };
    show();
    expect(await screen.findByRole("button", { name: "Turn on notifications on this device" })).toBeInTheDocument();
    expect(screen.getByText("Notifications are on for your other devices, not this one.")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Other devices: 2" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Turn off notifications everywhere" })).toBeInTheDocument();
  });

  it("blocked: the undo steps, no button", async () => {
    show({ permission: "denied" });
    const c = await card();
    expect(c).toHaveTextContent("This device is blocking notifications from Fold. To allow them again:");
    expect(c).toHaveTextContent("Click the icon left of the address bar, find Notifications, and set it to Allow.");
    expect(within(c).getByRole("link", { name: "More help with notifications" })).toHaveAttribute("href", "/notifications/setup");
    expect(within(c).queryByRole("button")).not.toBeInTheDocument();
  });

  it("blocked on an iPhone names the church in Settings", async () => {
    show({ permission: "denied", userAgent: UA.iphone, standalone: true });
    expect(await screen.findByText(/Open Settings → Notifications → Grace Church/)).toBeInTheDocument();
  });

  it("church off: no card at all", async () => {
    api.config.mockResolvedValue({ enabled: false, publicKey: "AQID" });
    show();
    await waitFor(() => expect(api.config).toHaveBeenCalled());
    await waitFor(() => expect(screen.queryByText("Loading notifications on this device…")).not.toBeInTheDocument());
    expect(screen.queryByRole("region", { name: "Notifications on this device" })).not.toBeInTheDocument();
  });
});

describe("re-saving on load", () => {
  it("shows ready when the server refuses the saved subscription", async () => {
    push = { on: true, devices: [], kinds: kinds() };
    api.save.mockResolvedValue({ saved: false, message: "Try again in a few minutes." });
    show({ permission: "granted", subscription: fakeSub() });
    expect(await screen.findByRole("button", { name: "Turn on notifications" })).toBeInTheDocument();
    expect(screen.getByText("This device was set up before. Turn it on again.")).toBeInTheDocument();
  });

  it("doesn't turn push back on after Turn off everywhere elsewhere", async () => {
    push = { on: false, devices: [], kinds: kinds() };
    show({ permission: "granted", subscription: fakeSub() });
    expect(await screen.findByRole("button", { name: "Turn on notifications" })).toBeInTheDocument();
    expect(api.save).not.toHaveBeenCalled();
  });

  it("skips a subscription made with an older key", async () => {
    push = { on: true, devices: [], kinds: kinds() };
    show({ permission: "granted", subscription: fakeSub(undefined, [9, 9, 9]) });
    expect(await screen.findByRole("button", { name: "Turn on notifications" })).toBeInTheDocument();
    expect(api.save).not.toHaveBeenCalled();
  });
});

describe("the flows", () => {
  it("turns on: asks after the tap, registers /sw.js, subscribes with the key and saves", async () => {
    const b = show();
    await userEvent.click(await screen.findByRole("button", { name: "Turn on notifications" }));
    expect(await screen.findByRole("button", { name: "Send a test notification" })).toBeInTheDocument();
    expect(b.Notification.requestPermission).toHaveBeenCalledTimes(1);
    expect(b.serviceWorker.register).toHaveBeenCalledWith("/sw.js", { scope: "/" });
    const opts = (b.pushManager.subscribe.mock.calls[0] as unknown[])[0] as PushSubscriptionOptionsInit;
    expect(opts.userVisibleOnly).toBe(true);
    expect([...(opts.applicationServerKey as Uint8Array)]).toEqual([1, 2, 3]);
    expect(api.save).toHaveBeenCalledWith(b.current()!.toJSON());
    expect(screen.getByRole("status")).toHaveTextContent("Turned on. Send a test to check it works.");
  });

  it("shows the server's line when it saved nothing", async () => {
    api.save.mockResolvedValue({ saved: false, message: "Try again in a few minutes." });
    show();
    await userEvent.click(await screen.findByRole("button", { name: "Turn on notifications" }));
    expect(await screen.findByText("Try again in a few minutes.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Turn on notifications" })).toBeInTheDocument();
  });

  it("goes to blocked when the person says no, and stays ready when they dismiss", async () => {
    show({ answer: "denied" });
    await userEvent.click(await screen.findByRole("button", { name: "Turn on notifications" }));
    expect(await screen.findByText(/This device is blocking notifications/)).toBeInTheDocument();
    uninstallBrowser();
  });

  it("stays ready when the prompt is dismissed", async () => {
    const b = show({ answer: "default" });
    await userEvent.click(await screen.findByRole("button", { name: "Turn on notifications" }));
    expect(await screen.findByText("Notifications are still off. You can turn them on any time.")).toBeInTheDocument();
    expect(b.pushManager.subscribe).not.toHaveBeenCalled();
  });

  it("sends a test and shows sent:false lines as they are", async () => {
    push = { on: true, devices: [device(7, "Chrome on Windows")], kinds: kinds() };
    const sub = fakeSub();
    show({ permission: "granted", subscription: sub });
    api.test.mockResolvedValueOnce({ sent: true }).mockResolvedValueOnce({ sent: false, message: "You just sent a few tests. Try again in a few minutes." });
    const btn = await screen.findByRole("button", { name: "Send a test notification" });
    await userEvent.click(btn);
    expect(await screen.findByText("Sent. It should show on this device in a moment.")).toBeInTheDocument();
    expect(api.test).toHaveBeenCalledWith(sub.endpoint);
    await userEvent.click(btn);
    expect(await screen.findByText("You just sent a few tests. Try again in a few minutes.")).toBeInTheDocument();
  });

  it("turns off this device: deletes it on the server and unsubscribes the browser", async () => {
    push = { on: true, devices: [device(7, "Chrome on Windows")], kinds: kinds() };
    const sub = fakeSub();
    show({ permission: "granted", subscription: sub });
    await userEvent.click(await screen.findByRole("button", { name: "Turn off notifications on this device" }));
    expect(await screen.findByRole("button", { name: "Turn on notifications" })).toBeInTheDocument();
    expect(api.remove).toHaveBeenCalledWith(sub.endpoint);
    expect(sub.unsubscribe).toHaveBeenCalled();
    expect(screen.getByRole("status")).toHaveTextContent("Notifications are off for this device.");
  });

  it("turns off everywhere behind a confirm", async () => {
    push = { on: true, devices: [device(7, "Chrome on Windows"), device(3, "iPhone · Home Screen app")], kinds: kinds() };
    const sub = fakeSub();
    show({ permission: "granted", subscription: sub });
    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true);
    const btn = await screen.findByRole("button", { name: "Turn off notifications everywhere" });
    await userEvent.click(btn);
    expect(api.turnOffEverywhere).not.toHaveBeenCalled();
    await userEvent.click(btn);
    expect(confirm).toHaveBeenCalledTimes(2);
    expect(await screen.findByText("Notifications are off on all your devices.")).toBeInTheDocument();
    expect(sub.unsubscribe).toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Turn on notifications" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: /Other devices/ })).not.toBeInTheDocument();
  });

  it("saves a Push switch for one kind", async () => {
    push = { on: true, devices: [device(7, "Chrome on Windows")], kinds: kinds() };
    show({ permission: "granted", subscription: fakeSub() });
    const box = await screen.findByRole("checkbox", { name: /^Event updates/ });
    expect(box).toBeChecked();
    await userEvent.click(box);
    expect(api.updatePush).toHaveBeenCalledWith("events", true);
    await waitFor(() => expect(screen.getByRole("checkbox", { name: /^Event updates/ })).not.toBeChecked());
    expect(screen.getByRole("status")).toHaveTextContent("Saved");
  });
});
