import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { fakeSub, installBrowser, uninstallBrowser, type BrowserOptions } from "../test/push";

const site = vi.fn();
vi.mock("../api", async (orig) => ({ ...(await orig<typeof import("../api")>()), Site: { get: () => site() } }));
vi.mock("../auth", () => ({ useAuth: () => ({ user: null }) }));

import { NotificationsSetupPage } from "./NotificationsSetup";

const UA = {
  iphone: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
  oldIphone: "Mozilla/5.0 (iPhone; CPU iPhone OS 15_7 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/15.7 Mobile/15E148 Safari/604.1",
  android: "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Mobile Safari/537.36",
  macSafari: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.1 Safari/605.1.15",
};

beforeEach(() => site.mockReset().mockResolvedValue({ name: "Grace Church", timezone: "UTC", selfSignup: "off", roomsEnabled: false, pushEnabled: true }));
afterEach(() => uninstallBrowser());

function show(o: BrowserOptions = {}) {
  installBrowser(o);
  render(<MemoryRouter><NotificationsSetupPage /></MemoryRouter>);
  return screen.findByRole("heading", { name: "Get notifications on your phone", level: 1 });
}
const first = () => screen.getAllByRole("heading", { level: 2 })[0];

it("puts the iPhone steps first on an iPhone, with the other devices below", async () => {
  await show({ userAgent: UA.iphone, supported: false });
  expect(first()).toHaveTextContent("Get notifications on your iPhone or iPad");
  const mine = screen.getByRole("region", { name: "Get notifications on your iPhone or iPad" });
  expect(mine).toHaveTextContent("open Grace Church from your Home Screen");
  expect(within(mine).getByText("Using a different browser?")).toBeInTheDocument();
  expect(within(mine).getAllByRole("link", { name: "Me → Notifications" })[0]).toHaveAttribute("href", "/profile/notifications");
  const others = screen.getByRole("region", { name: "Using a different device?" });
  for (const h of ["Get notifications on your Android phone", "Get notifications on your computer", "Get notifications in Safari on your Mac"]) {
    expect(within(others).getByRole("heading", { name: h, level: 3 })).toBeInTheDocument();
  }
});

it("puts Android first on Android", async () => {
  await show({ userAgent: UA.android });
  expect(first()).toHaveTextContent("Get notifications on your Android phone");
  uninstallBrowser();
});

it("puts the computer first on a desktop browser", async () => {
  await show();
  expect(first()).toHaveTextContent("Get notifications on your computer");
});

it("puts Mac Safari first in Safari on a Mac", async () => {
  await show({ userAgent: UA.macSafari });
  expect(first()).toHaveTextContent("Get notifications in Safari on your Mac");
});

it("gives this device's undo steps at the top when it blocks notifications", async () => {
  await show({ userAgent: UA.android, permission: "denied" });
  expect(screen.getByText("This device is blocking notifications from Grace Church. To allow them again:")).toBeInTheDocument();
  // Once, at the top: not again in this device's section.
  expect(screen.getAllByText(/Tap the lock icon next to the address in Chrome, then/)).toHaveLength(1);
  expect(screen.queryByRole("heading", { name: "Blocked it by mistake?", level: 3 })).not.toBeInTheDocument();
});

it("says when notifications are already on in this browser", async () => {
  await show({ permission: "granted", subscription: fakeSub() });
  expect(await screen.findByText(/This browser is set up\. Check/)).toBeInTheDocument();
});

it("inside the Home Screen app, says where to go instead of how to install", async () => {
  await show({ userAgent: UA.iphone, standalone: true });
  const mine = screen.getByRole("region", { name: "Get notifications on your iPhone or iPad" });
  expect(mine).toHaveTextContent("You're in the Home Screen app. Go to Me → Notifications and tap Turn on notifications.");
  expect(within(mine).queryByText(/Add to Home Screen/)).not.toBeInTheDocument();
  // The install steps are still there, collapsed with the other devices.
  const others = screen.getByRole("region", { name: "Using a different device?" });
  expect(within(others).getByRole("heading", { name: "Get notifications on your iPhone or iPad", level: 3 })).toBeInTheDocument();
});

it("names the missing https on an insecure address", async () => {
  await show({ secure: false });
  expect(screen.getByText("This address isn't secure (https), so notifications can't work here. Open Fold at its https address, or use email.")).toBeInTheDocument();
});

it("says when this browser can't receive notifications", async () => {
  await show({ supported: false });
  expect(screen.getByText("This browser can't receive notifications. Open Fold in another browser, or use email.")).toBeInTheDocument();
});

it("collapses the other devices and closes with one shortcut line", async () => {
  await show();
  const others = screen.getByRole("region", { name: "Using a different device?" });
  for (const d of others.querySelectorAll("details")) expect(d.open).toBe(false);
  expect(screen.getAllByText(/Notifications are a shortcut/)).toHaveLength(1);
});

it("says when the iPhone is too old", async () => {
  await show({ userAgent: UA.oldIphone });
  expect(screen.getByText(/need iOS 16.4 or newer/)).toBeInTheDocument();
});

it("isn't there when the church turned notifications off (DEC-124)", async () => {
  site.mockResolvedValue({ name: "Grace Church", timezone: "UTC", selfSignup: "off", roomsEnabled: false, pushEnabled: false });
  installBrowser();
  render(<MemoryRouter><NotificationsSetupPage /></MemoryRouter>);
  expect(await screen.findByRole("heading", { name: "Page not found", level: 1 })).toBeInTheDocument();
  expect(screen.queryByText(/Get notifications/)).not.toBeInTheDocument();
});
