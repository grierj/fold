import { cleanup, render, screen } from "@testing-library/react";
import { RouterProvider, createMemoryRouter } from "react-router-dom";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api";
import { Messages } from "../messages-api";
import { polyfillEditorLayout } from "../test/editor";
import { MessageReadPage } from "./MessageRead";

vi.mock("../messages-api", () => ({ Messages: { read: vi.fn() } }));
const viewer = vi.hoisted(() => ({ isAdmin: false, canCompose: false }));
vi.mock("../auth", () => ({ useAuth: () => ({ user: { id: 3, name: "Sam" }, loading: false, ...viewer }) }));

beforeAll(polyfillEditorLayout);
afterEach(() => { vi.clearAllMocks(); viewer.isAdmin = false; viewer.canCompose = false; });

function open(path = "/messages/5/read") {
  const router = createMemoryRouter([{ path: "/messages/:id/read", element: <MessageReadPage /> }], { initialEntries: [path] });
  render(<RouterProvider router={router} />);
}

// The page an announcement's push opens (M3.13d2): what was sent, from
// whom, and when; nothing about who else got it.
describe("MessageReadPage", () => {
  it("shows the subject, who it's from, when, and the message", async () => {
    vi.mocked(Messages.read).mockResolvedValue({ message: {
      id: 5, subject: "Snow day", from: "Lee Leader via Grace", sentAt: "2026-10-30T16:30:00Z",
      bodyHtml: "<p>No meeting <strong>tonight</strong>.</p><script>alert(1)</script>",
    } });
    open();
    expect(await screen.findByRole("heading", { name: "Snow day", level: 1 })).toBeInTheDocument();
    expect(screen.getByText("From Lee Leader via Grace")).toBeInTheDocument();
    expect(screen.getByText(/^Sent /)).toBeInTheDocument();
    expect(await screen.findByText("tonight")).toBeInTheDocument();
    expect(Messages.read).toHaveBeenCalledWith(5);
    // The body goes through the editor's schema: no script survives.
    expect(document.querySelector("script")).toBeNull();
    expect(screen.queryByText(/who got it/i)).toBeNull();
  });

  it("says a message that isn't theirs isn't here", async () => {
    vi.mocked(Messages.read).mockRejectedValue(new ApiError(404, "message not found", { code: "not_found" }));
    open();
    expect(await screen.findByRole("heading", { name: "This message isn't here" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Go to home" })).toHaveAttribute("href", "/");
  });

  it("goes back to Messages for those who write them, and Home for everyone else", async () => {
    vi.mocked(Messages.read).mockResolvedValue({ message: { id: 5, subject: "Snow day", from: "Lee Leader via Grace", sentAt: null, bodyHtml: "<p>Hi</p>" } });
    open();
    await screen.findByRole("heading", { name: "Snow day", level: 1 });
    expect(screen.getByRole("link", { name: "Back to home" })).toHaveAttribute("href", "/");
    cleanup();
    viewer.canCompose = true;
    open();
    await screen.findByRole("heading", { name: "Snow day", level: 1 });
    expect(screen.getByRole("link", { name: "Back to messages" })).toHaveAttribute("href", "/messages");
  });
});
