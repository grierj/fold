import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Notification } from "../types";
import { NotificationsPage } from "./Notifications";

const api = vi.hoisted(() => ({
  list: vi.fn(), badge: vi.fn(), seen: vi.fn(), read: vi.fn(), readAll: vi.fn(), archive: vi.fn(), approveGroup: vi.fn(),
}));
vi.mock("../api", () => {
  class ApiError extends Error {}
  return { Notifications: api, ApiError };
});
vi.mock("../auth", () => ({ useAuth: () => ({ user: { id: 2 }, loading: false, isAdmin: true }) }));

const item = (over: Partial<Notification>): Notification => ({
  id: 1, kind: "group.leadership_changed", category: "admin", title: "Pat made Sam a leader", body: "Youth",
  link: "/groups/1", actions: [], batchCount: 1, createdAt: new Date().toISOString(), updatedAt: new Date().toISOString(),
  read: false, archived: false, waiting: false, ...over,
});

function open(path = "/notifications") {
  return render(<MemoryRouter initialEntries={[path]}><NotificationsPage /></MemoryRouter>);
}
function feed(notifications: Notification[], waiting: Notification[] = []) {
  api.list.mockResolvedValue({ waiting, waitingTotal: waiting.length, notifications, next: "" });
}

beforeEach(() => {
  Object.values(api).forEach((f) => f.mockReset());
  api.seen.mockResolvedValue({});
  api.readAll.mockResolvedValue({});
  api.badge.mockResolvedValue({ badge: { unread: 1, waiting: 0 }, etag: '"1"' });
  feed([item({})]);
});

it("lays out like other pages: title and chips on the page, the list in one card", async () => {
  const { container } = open();
  expect(screen.getByRole("heading", { level: 1, name: "Alerts" })).toBeInTheDocument();
  const group = screen.getByRole("group", { name: "Show" });
  expect(within(group).getByRole("button", { name: "All", pressed: true })).toBeInTheDocument();
  const link = await screen.findByRole("link", { name: /Pat made Sam a leader/ });
  const card = link.closest(".bg-surface");
  expect(card).toHaveClass("border", "rounded-lg");
  expect(card).not.toContainElement(screen.getByRole("heading", { level: 1 }));
  expect(card).not.toContainElement(group);
  expect(container.firstElementChild).toHaveClass("max-w-2xl", "mx-auto");
});

describe("Mark all as read", () => {
  it("works when something is unread", async () => {
    open();
    const btn = await screen.findByRole("button", { name: "Mark all as read" });
    await waitFor(() => expect(btn).toHaveAttribute("aria-disabled", "false"));
    await userEvent.click(btn);
    expect(api.readAll).toHaveBeenCalled();
  });

  it("is disabled at zero unread, even with a task waiting", async () => {
    api.badge.mockResolvedValue({ badge: { unread: 0, waiting: 1 }, etag: '"2"' });
    open();
    await screen.findByRole("link", { name: /Pat made Sam a leader/ });
    await waitFor(() => expect(api.badge).toHaveBeenCalled());
    const btn = screen.getByRole("button", { name: "Mark all as read" });
    expect(btn).toHaveAttribute("aria-disabled", "true");
    await userEvent.click(btn);
    expect(api.readAll).not.toHaveBeenCalled();
  });
});

it("links to the email settings", async () => {
  open();
  expect(await screen.findByRole("link", { name: "Email settings" })).toHaveAttribute("href", "/profile/notifications");
});

describe("icons by kind", () => {
  const cases: [string, string][] = [
    ["event.cancelled", "calendar"], ["event.changed", "calendar"], ["event.restored", "calendar"],
    ["occurrence.changed", "calendar"], ["registration.promoted", "calendar"], ["waitlist.cutoff", "calendar"],
    ["account.requested", "person"], ["add.admin_review", "person"], ["household.link_pending", "person"],
    ["group.pending", "group"], ["group.approved", "check"],
    ["membership.left", "group"], ["group.leadership_changed", "group"], ["group.rejected", "group"], ["group.deletion", "group"],
    ["teen_consent.changed", "shield"], ["contact.admin_review", "shield"],
    ["something.new", "generic"],
  ];
  it.each(cases)("%s uses the %s icon", async (kind, icon) => {
    feed([item({ kind, title: `Row for ${kind}` })]);
    open();
    const row = (await screen.findByRole("link", { name: new RegExp(`Row for ${kind}`) })).closest("li")!;
    const svg = row.querySelector("svg[data-icon]")!;
    expect(svg).toHaveAttribute("data-icon", icon);
    expect(svg).toHaveAttribute("aria-hidden", "true");
  });
});

describe("a resolved task", () => {
  const task = item({
    id: 5, kind: "household.link_pending", title: "A link needs you", body: "The link needs an admin to confirm it.",
  });

  it("shows only the outcome, not the stale body", async () => {
    feed([{ ...task, resolution: { outcome: "approved", by: "Pat", byMe: false, at: "" } }]);
    open();
    expect(await screen.findByText("Done · approved by Pat")).toBeInTheDocument();
    expect(screen.queryByText(/needs an admin to confirm/)).not.toBeInTheDocument();
  });

  it("keeps the body of an open one", async () => {
    feed([task]);
    open();
    expect(await screen.findByText(/needs an admin to confirm/)).toBeInTheDocument();
  });
});

describe("when empty", () => {
  it("says what would appear and offers the calendar", async () => {
    feed([]);
    open();
    expect(await screen.findByText("Updates about your groups and events show up here.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "See what's coming up" })).toHaveAttribute("href", "/calendar");
  });

  it("keeps the filters' own wording, with no calendar link", async () => {
    feed([]);
    open("/notifications?filter=archived");
    expect(await screen.findByText("Nothing archived.")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "See what's coming up" })).not.toBeInTheDocument();
  });
});
