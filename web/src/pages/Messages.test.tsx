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

const open = () => render(<MemoryRouter><MessagesPage /></MemoryRouter>);
afterEach(() => { vi.clearAllMocks(); actor.canCompose = true; });

describe("MessagesPage", () => {
  it("lists each message with its state in words, subject, audience and update time", async () => {
    vi.mocked(Messages.options).mockResolvedValue(composeOptions());
    vi.mocked(Messages.list).mockResolvedValue({ messages: [
      message({ id: 1, status: "scheduled", scheduledAt: "2030-11-05T16:30:00Z" }),
      message({ id: 2, subject: "", status: "draft", audience: { type: "event_series", eventId: 3, statuses: ["going"] }, authorName: "Grier Admin" }),
      message({ id: 3, status: "cancelled" }),
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
    expect(screen.getAllByRole("link", { name: "New message" })).toHaveLength(1);
    expect(screen.getByRole("link", { name: "New message" })).toHaveClass("btn-primary");
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
