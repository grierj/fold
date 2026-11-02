import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api";
import { Messages } from "../messages-api";
import { composeOptions, message, reminderMessage } from "../test/compose";
import { MessagesPage } from "./Messages";

const actor = vi.hoisted(() => ({ canCompose: true, isAdmin: false }));
vi.mock("../auth", () => ({ useAuth: () => ({ user: { id: 2, name: "Lee Leader", displayName: "" }, loading: false, isAdmin: actor.isAdmin, canCompose: actor.canCompose }) }));
vi.mock("../messages-api", () => ({ Messages: { list: vi.fn(), options: vi.fn() } }));
vi.mock("../api", async (importActual) => ({
  ...(await importActual<typeof import("../api")>()),
  Site: { get: vi.fn().mockResolvedValue({ name: "Grace", timezone: "America/Boise" }) },
}));

const firstPage = { limit: 25, cursor: "", search: "", status: "" };
const open = () => render(<MemoryRouter><MessagesPage /></MemoryRouter>);
afterEach(() => { vi.useRealTimers(); vi.clearAllMocks(); actor.canCompose = true; actor.isAdmin = false; });

describe("MessagesPage", () => {
  it("lists each message with its state in words, subject, audience and update time", async () => {
    vi.mocked(Messages.options).mockResolvedValue(composeOptions());
    vi.mocked(Messages.list).mockResolvedValue({ messages: [
      message({ id: 1, status: "scheduled", scheduledAt: "2030-11-05T16:30:00Z" }),
      message({ id: 2, subject: "", status: "draft", audience: { type: "event_series", eventId: 3, statuses: ["going"] }, authorName: "Grier Admin" }),
      message({ id: 3, status: "cancelled", scheduledAt: "2030-11-05T16:30:00Z" }),
      message({ id: 4, status: "cancelled" }),
      message({ id: 5, status: "scheduled", scheduledAt: "2026-01-01T16:00:00Z" }),
      message({ id: 6, status: "sending" }),
      message({ id: 7, status: "sent", sentAt: "2026-01-02T16:00:00Z" }),
      message({ id: 8, status: "sent", sentAt: "2026-01-02T16:00:00Z", failedCount: 2 }),
      message({ id: 9, status: "failed", error: "No email service is set up yet." }),
    ] });
    open();
    const links = await screen.findAllByRole("link");
    const rows = links.filter((a) => a.getAttribute("href")?.startsWith("/messages/") && a.getAttribute("href") !== "/messages/new");
    expect(rows[0]).toHaveTextContent(/^Scheduled for .*Bring a friendTo Book club \(Members, Leaders\)Updated /);
    expect(rows[0]).toHaveAttribute("href", "/messages/1");
    expect(rows[1]).toHaveTextContent("Draft");
    expect(rows[1]).toHaveTextContent("Untitled message");
    expect(rows[1]).toHaveTextContent("To Book night, all upcoming dates (Going)");
    expect(rows[1]).toHaveTextContent("By Grier Admin");
    expect(rows[2]).toHaveTextContent("Cancelled");
    expect(rows[3]).toHaveTextContent("Discarded");
    // Send now: the sender (M3.5) hasn't picked it up yet.
    expect(rows[4]).toHaveTextContent("Waiting to send");
    // After it goes out (M3.5b).
    expect(rows[5]).toHaveTextContent(/^Sending/);
    expect(rows[6]).toHaveTextContent(/^Sent /);
    expect(rows[7]).toHaveTextContent(/^Partly sent /);
    expect(rows[8]).toHaveTextContent(/^Failed/);
    expect(screen.queryByText(/Showing your latest/)).toBeNull();
    expect(screen.getAllByRole("link", { name: "New message" })).toHaveLength(1);
    expect(screen.getByRole("link", { name: "New message" })).toHaveClass("btn-primary");
  });

  it("pages to older history rather than hiding everything after 200", async () => {
    vi.mocked(Messages.options).mockResolvedValue(composeOptions());
    vi.mocked(Messages.list).mockResolvedValueOnce({ messages: Array.from({ length: 25 }, (_, i) => message({ id: i + 1 })), nextCursor: "older-page" })
      .mockResolvedValueOnce({ messages: [message({ id: 201, subject: "An older message" })] });
    open();
    await userEvent.click(await screen.findByRole("button", { name: "Older messages" }));
    expect(await screen.findByRole("link", { name: /An older message/ })).toHaveAttribute("href", "/messages/201");
    expect(Messages.list).toHaveBeenLastCalledWith(undefined, { ...firstPage, cursor: "older-page" });
    expect(screen.queryByRole("button", { name: "Older messages" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Newest messages" })).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveFocus();
  });

  it("cannot apply an old cursor while a new subject search is debouncing", async () => {
    vi.mocked(Messages.options).mockResolvedValue(composeOptions());
    vi.mocked(Messages.list).mockResolvedValue({ messages: [message()], nextCursor: "old-query-cursor" });
    open();
    await screen.findByRole("button", { name: "Older messages" });
    vi.useFakeTimers();
    try {
      fireEvent.change(screen.getByRole("searchbox"), { target: { value: "Needle" } });
      const older = screen.getByRole("button", { name: "Older messages" });
      expect(older).toBeDisabled();
      fireEvent.click(older);
      await act(async () => vi.advanceTimersByTimeAsync(300));
      expect(Messages.list).toHaveBeenLastCalledWith(undefined, { ...firstPage, search: "Needle" });
      expect(vi.mocked(Messages.list).mock.calls.every(([, options]) => !options?.cursor)).toBe(true);
    } finally { vi.useRealTimers(); }
  });

  it("offers a way out of an invalid history cursor", async () => {
    vi.mocked(Messages.options).mockResolvedValue(composeOptions());
    vi.mocked(Messages.list).mockRejectedValueOnce(new ApiError(400, "bad cursor")).mockResolvedValueOnce({ messages: [message()] });
    render(<MemoryRouter initialEntries={["/messages?cursor=bad"]}><MessagesPage /></MemoryRouter>);
    await screen.findByRole("alert");
    await userEvent.click(screen.getByRole("button", { name: "Newest messages" }));
    expect(await screen.findByRole("link", { name: /Bring a friend/ })).toBeInTheDocument();
    expect(Messages.list).toHaveBeenLastCalledWith(undefined, firstPage);
  });

  it("says what would appear and offers New message when there are none", async () => {
    vi.mocked(Messages.options).mockResolvedValue(composeOptions());
    vi.mocked(Messages.list).mockResolvedValue({ messages: [] });
    open();
    expect(await screen.findByText("No messages yet")).toBeInTheDocument();
    expect(screen.getByText(/drafts, scheduled ones and sent ones/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "New message" })).toHaveAttribute("href", "/messages/new");
  });

  it("tells someone who can't compose who the page is for", async () => {
    actor.canCompose = false;
    vi.mocked(Messages.options).mockResolvedValue(composeOptions({ canCompose: false, groups: [], events: [] }));
    vi.mocked(Messages.list).mockResolvedValue({ messages: [] });
    open();
    expect(await screen.findByText("Messages are for leaders")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "New message" })).not.toBeInTheDocument();
  });

  it("shows a load failure in words with Try again", async () => {
    vi.mocked(Messages.options).mockResolvedValue(composeOptions());
    vi.mocked(Messages.list).mockRejectedValue(new ApiError(500, "boom", { code: "server_error" }));
    open();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("We couldn't load your messages. Please try again in a minute.");
    expect(within(alert).getByRole("button", { name: "Try again" })).toBeInTheDocument();
    expect(alert).not.toHaveTextContent("boom");
  });

  it("gives an admin Announcements and Automatic reminders chips, each its own list (M3.6b)", async () => {
    actor.isAdmin = true;
    vi.mocked(Messages.options).mockResolvedValue(composeOptions());
    vi.mocked(Messages.list).mockImplementation(async (category) => ({ messages: category === "reminder" ? [
      reminderMessage(),
      reminderMessage({ id: 10, status: "failed", sentAt: null, reminder: { ...reminderMessage().reminder!, kind: "waitlist" } }),
    ] : [message({ id: 1 })] }));
    open();
    const announcements = await screen.findByRole("button", { name: "Announcements" });
    expect(announcements).toHaveAttribute("aria-pressed", "true");
    expect(Messages.list).toHaveBeenLastCalledWith("announcement", firstPage);
    expect(await screen.findByRole("link", { name: /Bring a friend/ })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Automatic reminder/ })).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Automatic reminders" }));
    expect(Messages.list).toHaveBeenLastCalledWith("reminder", firstPage);
    expect(screen.getByRole("button", { name: "Automatic reminders" })).toHaveAttribute("aria-pressed", "true");
    const row = await screen.findByRole("link", { name: /Automatic reminder ·/ });
    expect(row).toHaveAttribute("href", "/messages/9");
    // The date reads in the event's zone (America/Boise).
    expect(row).toHaveTextContent(/^Sent .*Automatic reminder · Book night, Thu, Nov 5(, 2026)?To everyone goingUpdated /);
    expect(row).not.toHaveTextContent("{{");
    expect(row).not.toHaveTextContent("By ");
    expect(screen.getByRole("link", { name: /Automatic waitlist note/ })).toHaveTextContent(/^FailedAutomatic waitlist note · Book night, Thu, Nov 5(, 2026)?To everyone on the waitlist/);
    expect(screen.queryByRole("link", { name: /Bring a friend/ })).toBeNull();
  });

  it("says what would appear when there are no automatic reminders yet", async () => {
    actor.isAdmin = true;
    vi.mocked(Messages.options).mockResolvedValue(composeOptions());
    vi.mocked(Messages.list).mockResolvedValue({ messages: [] });
    render(<MemoryRouter initialEntries={["/messages?show=reminders"]}><MessagesPage /></MemoryRouter>);
    expect(await screen.findByText("No automatic reminders yet")).toBeInTheDocument();
    expect(Messages.list).toHaveBeenCalledWith("reminder", firstPage);
  });

  it("shows a leader no chips and their own list", async () => {
    vi.mocked(Messages.options).mockResolvedValue(composeOptions());
    vi.mocked(Messages.list).mockResolvedValue({ messages: [message({ id: 1 })] });
    open();
    expect(await screen.findByRole("link", { name: /Bring a friend/ })).toBeInTheDocument();
    expect(Messages.list).toHaveBeenCalledWith(undefined, firstPage);
    expect(screen.queryByRole("button", { name: "Automatic reminders" })).toBeNull();
  });
});
