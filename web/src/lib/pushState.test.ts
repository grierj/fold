import { afterEach, describe, expect, it, vi } from "vitest";
import { cardState, deviceState, readPushEnv, sameKey, type PushEnv } from "./pushState";

const UA = {
  iphone163: "Mozilla/5.0 (iPhone; CPU iPhone OS 16_3 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.3 Mobile/15E148 Safari/604.1",
  iphone17: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
  ipadDesktop: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.1 Safari/605.1.15",
  android: "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Mobile Safari/537.36",
  macChrome: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36",
  windowsEdge: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36 Edg/126.0",
};

// What readPushEnv makes of a browser's user agent.
function envFor(ua: string, touchPoints = 0) {
  vi.spyOn(navigator, "userAgent", "get").mockReturnValue(ua);
  Object.defineProperty(navigator, "maxTouchPoints", { value: touchPoints, configurable: true });
  return readPushEnv();
}

describe("platform and iOS version", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    Reflect.deleteProperty(navigator, "maxTouchPoints");
  });

  it("reads the platform, including an iPad asking for desktop pages", () => {
    expect(envFor(UA.iphone17).platform).toBe("ios");
    expect(envFor(UA.ipadDesktop, 5).platform).toBe("ios");
    expect(envFor(UA.ipadDesktop, 0).platform).toBe("mac-safari");
    expect(envFor(UA.android).platform).toBe("android");
    expect(envFor(UA.macChrome).platform).toBe("computer");
    expect(envFor(UA.windowsEdge).platform).toBe("computer");
  });

  it("reads the iOS version from the OS token or Safari's version", () => {
    expect(envFor(UA.iphone163).iosVersion).toEqual([16, 3]);
    expect(envFor(UA.ipadDesktop).iosVersion).toEqual([17, 1]);
    expect(envFor("Mozilla/5.0 (X11; Linux x86_64)").iosVersion).toBeNull();
  });
});

const env = (over: Partial<PushEnv>): PushEnv => ({
  secure: true, supported: true, permission: "default", standalone: false, platform: "computer", iosVersion: null, ...over,
});

describe("deviceState", () => {
  it("needs a secure context and a browser that can push", () => {
    expect(deviceState(env({ secure: false }))).toBe("unavailable");
    expect(deviceState(env({ supported: false }))).toBe("unavailable");
    expect(deviceState(env({}))).toBe("available");
    expect(deviceState(env({ permission: "granted" }))).toBe("available");
    expect(deviceState(env({ permission: "denied" }))).toBe("blocked");
  });

  it("asks iPhones to add Fold to the Home Screen, and says when iOS is too old", () => {
    expect(deviceState(env({ platform: "ios", iosVersion: [17, 5], supported: false }))).toBe("install_first");
    expect(deviceState(env({ platform: "ios", iosVersion: [16, 3], supported: false }))).toBe("unavailable_ios");
    expect(deviceState(env({ platform: "ios", iosVersion: [16, 3], standalone: true }))).toBe("unavailable_ios");
    expect(deviceState(env({ platform: "ios", iosVersion: [16, 4], standalone: true }))).toBe("available");
    expect(deviceState(env({ platform: "ios", iosVersion: null, standalone: true, permission: "denied" }))).toBe("blocked");
  });
});

describe("cardState", () => {
  const facts = { enabled: true, device: "available" as const, thisDeviceOn: false, otherDevices: 0 };
  it("puts the church's switch first, then the device, then the server's devices", () => {
    expect(cardState({ ...facts, enabled: false, device: "blocked" })).toBe("church_off");
    expect(cardState({ ...facts, device: "install_first" })).toBe("install_first");
    expect(cardState(facts)).toBe("ready");
    expect(cardState({ ...facts, otherDevices: 2 })).toBe("on_elsewhere");
    expect(cardState({ ...facts, thisDeviceOn: true, otherDevices: 2 })).toBe("on");
  });
});

it("matches a subscription to the church's current key", () => {
  const sub = (key: number[] | null) => ({ options: { applicationServerKey: key && new Uint8Array(key).buffer } }) as unknown as PushSubscription;
  expect(sameKey(sub([1, 2, 3]), "AQID")).toBe(true);
  expect(sameKey(sub([1, 2, 4]), "AQID")).toBe(false);
  expect(sameKey(sub(null), "AQID")).toBe(true);
});
