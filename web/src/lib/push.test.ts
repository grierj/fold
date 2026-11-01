import { afterEach, describe, expect, it, vi } from "vitest";
import { syncAppBadge } from "./push";

describe("syncAppBadge", () => {
  afterEach(() => {
    delete (navigator as unknown as Record<string, unknown>).setAppBadge;
    delete (navigator as unknown as Record<string, unknown>).clearAppBadge;
  });
  it("sets the icon's count and clears it at 0, where the browser can", () => {
    syncAppBadge(3); // no Badging API: nothing happens, nothing throws
    const set = vi.fn(async () => {});
    const clear = vi.fn(async () => {});
    Object.assign(navigator, { setAppBadge: set, clearAppBadge: clear });
    syncAppBadge(3);
    expect(set).toHaveBeenCalledWith(3);
    syncAppBadge(0);
    expect(clear).toHaveBeenCalled();
  });
});
