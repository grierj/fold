import { afterEach, describe, expect, it, vi } from "vitest";
import { Auth, Groups, setOnUnauthorized } from "./api";

describe("api 401 handling", () => {
  afterEach(() => {
    setOnUnauthorized(null);
    vi.unstubAllGlobals();
  });

  it("sends people to log in on a 401, except for the boot-time session check", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", { status: 401 })));
    const onUnauthorized = vi.fn();
    setOnUnauthorized(onUnauthorized);

    await expect(Auth.me()).rejects.toMatchObject({ status: 401 });
    expect(onUnauthorized).not.toHaveBeenCalled(); // signed-out visitors keep public pages

    await expect(Groups.mine()).rejects.toMatchObject({ status: 401 });
    expect(onUnauthorized).toHaveBeenCalledOnce();
  });
});
