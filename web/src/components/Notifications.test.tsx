import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Notification } from "../types";
import { useBadge } from "../lib/notifications";
import { NotificationBell, NotificationTab } from "./Notifications";

const api = vi.hoisted(() => ({
  list: vi.fn(),
  badge: vi.fn(),
  seen: vi.fn(),
  read: vi.fn(),
  readAll: vi.fn(),
  archive: vi.fn(),
  approveGroup: vi.fn(),
}));
vi.mock("../api", () => {
  class ApiError extends Error {
    status: number;
    data: Record<string, unknown>;
    constructor(status: number, message: string, data: Record<string, unknown> = {}) {
      super(message);
      this.status = status;
      this.data = data;
    }
  }
  return { Notifications: api, ApiError };
});

const item = (over: Partial<Notification>): Notification => ({
  id: 1, kind: "group.leadership_changed", category: "admin", title: "Pat made Sam a leader", body: "Youth",
  link: "/groups/1", actions: [], batchCount: 1, createdAt: new Date().toISOString(), updatedAt: new Date().toISOString(),
  read: false, archived: false, waiting: false, ...over,
});

const task = item({
  id: 2, kind: "group.pending", subjectId: 9, title: "Sam started a group: Breakfast", waiting: true,
  actions: [{ key: "approve", label: "Approve" }], link: "/admin?tab=approvals",
});

// Layout owns the badge poller and hands the result to both.
function Bell() {
  return <NotificationBell badge={useBadge("")} />;
}
function Tab() {
  return <NotificationTab badge={useBadge("")} className={() => ""} />;
}

function renderBell() {
  return render(
    <MemoryRouter>
      <Bell />
    </MemoryRouter>
  );
}

beforeEach(() => {
  Object.values(api).forEach((f) => f.mockReset());
  api.seen.mockResolvedValue({});
  api.read.mockResolvedValue({});
  api.badge.mockResolvedValue({ badge: { unread: 1, waiting: 1 }, etag: '"1-1"' });
  api.list.mockResolvedValue({ waiting: [task], waitingTotal: 1, notifications: [item({})], next: "" });
  localStorage.clear();
});
afterEach(() => vi.useRealTimers());

it("includes the phone tab's visible count and caption in its accessible name", async () => {
  render(<MemoryRouter><Tab /></MemoryRouter>);
  const tab = await screen.findByRole("link", { name: "2 Alerts, 1 unread, 1 waiting on you" });
  expect(tab.textContent).toBe("2 Alerts");
});

describe("NotificationBell", () => {
  it("keeps section headings under the bell's second-level title", async () => {
    renderBell();
    await userEvent.click(await screen.findByRole("button", { name: /Alerts/ }));
    expect(await screen.findByRole("heading", { name: "Waiting on you", level: 3 })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Recent", level: 3 })).toBeInTheDocument();
  });
  it("includes its visible summed count and describes both numbers", async () => {
    renderBell();
    const bell = await screen.findByRole("button", { name: "2 Alerts, 1 unread, 1 waiting on you" });
    expect(within(bell).getByText("2")).toBeInTheDocument();
  });

  it("caps the badge at 9+ and shows nothing at zero", async () => {
    api.badge.mockResolvedValue({ badge: { unread: 8, waiting: 3 }, etag: "x" });
    const { unmount } = renderBell();
    const bell = await screen.findByRole("button", { name: "9+ Alerts, 8 unread, 3 waiting on you" });
    expect(within(bell).getByText("9+")).toBeInTheDocument();
    unmount();
    api.badge.mockResolvedValue({ badge: { unread: 0, waiting: 0 }, etag: "y" });
    renderBell();
    expect(await screen.findByRole("button", { name: "Alerts" })).toBeInTheDocument();
    expect(screen.queryByText("0")).toBeNull();
  });

  it("puts the count on the app icon where the browser can, and clears it at 0", async () => {
    const set = vi.fn(async () => {});
    const clear = vi.fn(async () => {});
    Object.assign(navigator, { setAppBadge: set, clearAppBadge: clear });
    try {
      const { unmount } = renderBell();
      await screen.findByText("2");
      expect(set).toHaveBeenLastCalledWith(2);
      unmount();
      api.badge.mockResolvedValue({ badge: { unread: 0, waiting: 0 }, etag: "z" });
      renderBell();
      await screen.findByRole("button", { name: "Alerts" });
      expect(clear).toHaveBeenCalled();
    } finally {
      delete (navigator as unknown as Record<string, unknown>).setAppBadge;
      delete (navigator as unknown as Record<string, unknown>).clearAppBadge;
    }
  });

  it("refetches on focus, sends its ETag, and keeps the count on 304", async () => {
    renderBell();
    await screen.findByText("2");
    api.badge.mockResolvedValue(null);
    await act(async () => {
      fireEvent.focus(window);
    });
    expect(api.badge).toHaveBeenLastCalledWith('"1-1"');
    expect(screen.getByText("2")).toBeInTheDocument();
  });

  it("polls every 60 seconds and pauses while hidden", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    renderBell();
    await act(async () => {});
    const calls = api.badge.mock.calls.length;
    await act(async () => {
      vi.advanceTimersByTime(60_000);
    });
    expect(api.badge.mock.calls.length).toBe(calls + 1);
    Object.defineProperty(document, "visibilityState", { value: "hidden", configurable: true });
    await act(async () => {
      vi.advanceTimersByTime(60_000);
    });
    expect(api.badge.mock.calls.length).toBe(calls + 1);
    Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
  });

  it("lists waiting items first, approves inline, and keeps nothing in storage", async () => {
    api.approveGroup.mockImplementation(async () => {
      // The server now has it resolved by me; the refetch shows that.
      const done = { ...task, waiting: false, actions: [], resolution: { outcome: "approved", by: "Me", byMe: true, at: "" } };
      api.list.mockResolvedValue({ waiting: [], waitingTotal: 0, notifications: [done, item({})], next: "" });
      return {};
    });
    renderBell();
    await userEvent.click(await screen.findByRole("button", { name: /Alerts/ }));
    expect(await screen.findByText("Waiting on you", { selector: "h3" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Approve" }));
    expect(api.approveGroup).toHaveBeenCalledWith(9, 2);
    expect(await screen.findByText("You approved this")).toBeInTheDocument();
    expect(localStorage.length).toBe(0);
    expect(sessionStorage.length).toBe(0);
  });

  it("shows who won when an inline approve loses the race", async () => {
    const { ApiError } = await import("../api");
    api.approveGroup.mockImplementation(async () => {
      const done = { ...task, waiting: false, actions: [], resolution: { outcome: "approved", by: "Kim", byMe: false, at: "" } };
      api.list.mockResolvedValue({ waiting: [], waitingTotal: 0, notifications: [done], next: "" });
      throw new ApiError(409, "handled", { resolvedBy: "Kim", resolution: "approved" });
    });
    renderBell();
    await userEvent.click(await screen.findByRole("button", { name: /Alerts/ }));
    await userEvent.click(await screen.findByRole("button", { name: "Approve" }));
    expect(await screen.findByText("Already approved by Kim")).toBeInTheDocument();
  });

  it("archives with × and offers Undo", async () => {
    api.archive.mockResolvedValue({});
    renderBell();
    await userEvent.click(await screen.findByRole("button", { name: /Alerts/ }));
    await userEvent.click(await screen.findByRole("button", { name: "Archive: Pat made Sam a leader" }));
    expect(api.archive).toHaveBeenCalledWith(1, true);
    await userEvent.click(await screen.findByRole("button", { name: "Undo" }));
    expect(api.archive).toHaveBeenLastCalledWith(1, false);
  });

  it("Escape closes the panel and returns focus to the bell", async () => {
    renderBell();
    const bell = await screen.findByRole("button", { name: /Alerts/ });
    await userEvent.click(bell);
    expect(bell).toHaveAttribute("aria-expanded", "true");
    await userEvent.keyboard("{Escape}");
    expect(bell).toHaveAttribute("aria-expanded", "false");
    expect(bell).toHaveFocus();
  });
});
