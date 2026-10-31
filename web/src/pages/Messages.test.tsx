import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api";
import { Messages } from "../messages-api";
import { composeOptions, message } from "../test/compose";
import { MessagesPage } from "./Messages";

const actor = vi.hoisted(() => ({ canCompose: true }));
vi.mock("../auth", () => ({ useAuth: () => ({ user: { id: 2, name: "Lee Leader", displayName: "" }, loading: false, isAdmin: false, canCompose: actor.canCompose }) }));
vi.mock("../messages-api", () => ({ Messages: { list: vi.fn(), options: vi.fn() } }));
vi.mock("../api", async (importActual) => ({
  ...(await importActual<typeof import("../api")>()),
  Site: { get: vi.fn().mockResolvedValue({ name: "Grace", timezone: "America/Boise" }) },
}));

const open = () => render(<MemoryRouter><MessagesPage /></MemoryRouter>);
afterEach(() => { vi.clearAllMocks(); actor.canCompose = true; });

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

  it("says when only the latest 200 are shown", async () => {
    vi.mocked(Messages.options).mockResolvedValue(composeOptions());
    vi.mocked(Messages.list).mockResolvedValue({ messages: Array.from({ length: 200 }, (_, i) => message({ id: i + 1 })) });
    open();
    expect(await screen.findByText("Showing your latest 200 messages.")).toBeInTheDocument();
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
});
