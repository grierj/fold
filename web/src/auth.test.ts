import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Auth as AuthApi, ApiError } from "./api";
import { AuthProvider, clearClientData, useAuth } from "./auth";
import type { User } from "./types";
import { fakeSub, installBrowser, uninstallBrowser } from "./test/push";

vi.mock("./api", async (actual) => ({
  ...(await actual<typeof import("./api")>()),
  Auth: { me: vi.fn(), login: vi.fn(), logout: vi.fn() },
  ensureCsrf: vi.fn(async () => {}),
}));

it("clears stale adult auth when a pending-teen login ends the server session", async () => {
  const adult: User = { id: 1, name: "Pat", displayName: "Pat", email: "pat@example.com", phone: "", hideContactFromLeaders: false, avatarUrl: "", avatarFocalX: 50, avatarFocalY: 50, role: "member", status: "active", hasPassword: true, accountType: "adult", timezone: "UTC" };
  vi.mocked(AuthApi.me).mockResolvedValue({ user: adult });
  vi.mocked(AuthApi.login).mockRejectedValue(new ApiError(403, "private", { code: "consent_required" }));
  const { result } = renderHook(useAuth, { wrapper: AuthProvider });
  await waitFor(() => expect(result.current.user?.id).toBe(1));
  await act(async () => {
    await expect(result.current.login("teen@example.com", "password1234")).rejects.toMatchObject({ status: 403 });
  });
  expect(result.current.user).toBeNull();
});

describe("clearClientData", () => {
  it("empties local and session storage", () => {
    localStorage.setItem("fold-dismissed-notifications:7", "[]");
    sessionStorage.setItem("anything", "x");
    clearClientData();
    expect(localStorage.length + sessionStorage.length).toBe(0);
  });
});

it("says whether the person can compose, from the session and from signing in", async () => {
  const adult: User = { id: 1, name: "Lee", displayName: "Lee", email: "lee@example.com", phone: "", hideContactFromLeaders: false, avatarUrl: "", avatarFocalX: 50, avatarFocalY: 50, role: "member", status: "active", hasPassword: true, accountType: "adult", timezone: "UTC" };
  vi.mocked(AuthApi.me).mockResolvedValue({ user: adult, canCompose: false });
  vi.mocked(AuthApi.login).mockResolvedValue({ user: adult, canCompose: true });
  const { result } = renderHook(useAuth, { wrapper: AuthProvider });
  await waitFor(() => expect(result.current.user?.id).toBe(1));
  expect(result.current.canCompose).toBe(false);
  await act(async () => { await result.current.login("lee@example.com", "password1234"); });
  expect(result.current.canCompose).toBe(true);
});

describe("logout", () => {
  afterEach(() => {
    uninstallBrowser();
    vi.restoreAllMocks();
  });

  it("sends this browser's push endpoint, then drops the subscription and closes what's on screen", async () => {
    const sub = fakeSub("https://fcm.googleapis.com/fcm/send/logout-test");
    const browser = installBrowser({ permission: "granted", subscription: sub });
    const order: string[] = [];
    vi.mocked(AuthApi.me).mockResolvedValue({ user: null as unknown as User });
    vi.mocked(AuthApi.logout).mockImplementation(async () => { order.push("logout"); return {}; });
    sub.unsubscribe.mockImplementation(async () => { order.push("unsubscribe"); return true; });
    vi.spyOn(console, "error").mockImplementation(() => {}); // jsdom: "navigation not implemented"
    const { result } = renderHook(useAuth, { wrapper: AuthProvider });
    await waitFor(() => expect(result.current.loading).toBe(false));
    await act(async () => { await result.current.logout(); });
    expect(AuthApi.logout).toHaveBeenCalledWith(sub.endpoint);
    expect(order).toEqual(["logout", "unsubscribe"]);
    expect(browser.shown.every((n) => n.close.mock.calls.length === 1)).toBe(true);
    expect(browser.serviceWorker.register).not.toHaveBeenCalled();
  });

  it("sends no endpoint from a browser without notifications", async () => {
    vi.mocked(AuthApi.me).mockResolvedValue({ user: null as unknown as User });
    vi.mocked(AuthApi.logout).mockResolvedValue({});
    vi.spyOn(console, "error").mockImplementation(() => {}); // jsdom: "navigation not implemented"
    const { result } = renderHook(useAuth, { wrapper: AuthProvider });
    await waitFor(() => expect(result.current.loading).toBe(false));
    await act(async () => { await result.current.logout(); });
    expect(AuthApi.logout).toHaveBeenCalledWith(undefined);
  });
});
