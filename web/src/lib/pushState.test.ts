import { describe, expect, it } from "vitest";
import { cardState, deviceState, iosVersion, platformOf, sameKey, type PushEnv } from "./pushState";

const UA = {
  iphone163: "Mozilla/5.0 (iPhone; CPU iPhone OS 16_3 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.3 Mobile/15E148 Safari/604.1",
  iphone17: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
  ipadDesktop: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.1 Safari/605.1.15",
  android: "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Mobile Safari/537.36",
  macChrome: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36",
  windowsEdge: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36 Edg/126.0",
};

describe("platform and iOS version", () => {
  it("reads the platform, including an iPad asking for desktop pages", () => {
    expect(platformOf(UA.iphone17)).toBe("ios");
    expect(platformOf(UA.ipadDesktop, 5)).toBe("ios");
    expect(platformOf(UA.ipadDesktop, 0)).toBe("mac-safari");
    expect(platformOf(UA.android)).toBe("android");
    expect(platformOf(UA.macChrome)).toBe("computer");
    expect(platformOf(UA.windowsEdge)).toBe("computer");
  });

  it("reads the iOS version from the OS token or Safari's version", () => {
    expect(iosVersion(UA.iphone163)).toEqual([16, 3]);
    expect(iosVersion(UA.ipadDesktop)).toEqual([17, 1]);
    expect(iosVersion("Mozilla/5.0 (X11; Linux x86_64)")).toBeNull();
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
