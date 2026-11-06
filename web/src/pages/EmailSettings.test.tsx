import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import type { EmailSettingsChange } from "../api";
import type { NotificationSettings } from "../types";

const get = vi.fn<typeof import("../api").EmailSettingsLink.get>();
const update = vi.fn<typeof import("../api").EmailSettingsLink.update>();
vi.mock("../api", async (orig) => ({
  ...(await orig<typeof import("../api")>()),
  EmailSettingsLink: { get: (t: string) => get(t), update: (t: string, c: EmailSettingsChange) => update(t, c) },
}));

let signedIn = false;
vi.mock("../auth", async (orig) => ({
  ...(await orig<typeof import("../auth")>()),
  useAuth: () => ({ user: signedIn ? { id: 1, name: "Sam" } : null }),
}));

import { ApiError } from "../api";
import { EmailSettingsPage } from "./EmailSettings";

const kinds = [
  { category: "groups", off: false },
  { category: "events", off: false },
  { category: "messages", off: false },
] satisfies NotificationSettings["kinds"];
const turnOnMessage = "This link can only turn email off. To turn email back on, sign in, or use the link in a newer email from us.";

beforeEach(() => {
  signedIn = false;
  get.mockReset().mockResolvedValue({ emailSettings: { kinds, optionalEmailOff: false }, email: "s•••@example.com", fromCategory: "events", canTurnOn: true });
  update.mockReset().mockImplementation(async (_t: string, c: EmailSettingsChange) => ({
    emailSettings: { kinds: kinds.map((k) => (!c.category || k.category === c.category ? { ...k, off: c.off } : k)), optionalEmailOff: !c.category && c.off },
  }));
});

function open(token = "tok") {
  render(<MemoryRouter initialEntries={[`/email-settings/${token}`]}><Routes><Route path="/email-settings/:token" element={<EmailSettingsPage />} /></Routes></MemoryRouter>);
}

it("opens without signing in: the masked address and the kind the link came from, first", async () => {
  open("abc");
  expect(await screen.findByText("For s•••@example.com")).toBeInTheDocument();
  expect(screen.getByText(/You came here from an event update/)).toBeInTheDocument();
  const boxes = await screen.findAllByRole("checkbox");
  expect(boxes[0]).toHaveAccessibleName(/^Event updates/);
  expect(get).toHaveBeenCalledOnce();
  expect(get).toHaveBeenCalledWith("abc");
  expect(screen.getByRole("link", { name: "Sign in to see all your settings" })).toHaveAttribute("href", "/login");
  expect(screen.queryByText(turnOnMessage)).not.toBeInTheDocument();
});

it("explains a link that can only turn email off, above the unchanged switches", async () => {
  get.mockResolvedValue({ emailSettings: { kinds, optionalEmailOff: false }, email: "s•••@example.com", fromCategory: "events", canTurnOn: false });
  open();
  const message = await screen.findByText(turnOnMessage);
  const options = await screen.findByRole("group", { name: "Optional email" });
  expect(message).toHaveClass("text-text-muted");
  expect(message.compareDocumentPosition(options) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  expect(screen.getByRole("checkbox", { name: /^Event updates/ })).toBeEnabled();
  expect(screen.getByRole("checkbox", { name: /^Turn off all optional email/ })).toBeEnabled();
});

it("reverts a refused turn-on and announces the error while keeping turn-off available", async () => {
  get.mockResolvedValue({ emailSettings: { kinds: kinds.map((k) => ({ ...k, off: k.category === "events" })), optionalEmailOff: false }, email: "s•••@example.com", fromCategory: "events", canTurnOn: false });
  update.mockRejectedValueOnce(new ApiError(403, "not allowed", { code: "email_link_turn_on" }));
  const user = userEvent.setup();
  open("old");
  const events = await screen.findByRole("checkbox", { name: /^Event updates/ });
  expect(events).not.toBeChecked();
  await user.click(events);
  expect(update).toHaveBeenCalledWith("old", { category: "events", off: false });
  expect(await screen.findByRole("alert")).toHaveTextContent(turnOnMessage);
  expect(events).not.toBeChecked();
  expect(events).toHaveFocus();
  expect(screen.getByRole("status")).toBeEmptyDOMElement();

  update.mockResolvedValueOnce({ emailSettings: { kinds: kinds.map((k) => ({ ...k, off: k.category !== "messages" })), optionalEmailOff: false } });
  await user.click(screen.getByRole("checkbox", { name: /^Group updates/ }));
  expect(update).toHaveBeenLastCalledWith("old", { category: "groups", off: true });
  expect(await screen.findByText("Saved")).toBeInTheDocument();
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  expect(events).not.toBeChecked();
});

it("saves through the link", async () => {
  open("abc");
  await userEvent.click(await screen.findByRole("checkbox", { name: /^Group updates/ }));
  expect(update).toHaveBeenCalledWith("abc", { category: "groups", off: true });
  expect(await screen.findByText("Saved")).toBeInTheDocument();
});

it("says plainly when the link no longer works, never in red", async () => {
  get.mockRejectedValue(new ApiError(404, "not found", { code: "not_found" }));
  open("bad");
  expect(await screen.findByText("This link doesn't work anymore")).toBeInTheDocument();
  expect(screen.getByText("Sign in to change your email settings.")).toBeInTheDocument();
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
});

it("sends a signed-in visitor to their own notification settings", async () => {
  signedIn = true;
  open("abc");
  expect(await screen.findByRole("link", { name: "See all your settings" })).toHaveAttribute("href", "/profile/notifications");
  expect(screen.queryByRole("link", { name: "Sign in to see all your settings" })).not.toBeInTheDocument();
});

it("words the kinds as the email footer does", async () => {
  get.mockResolvedValue({ emailSettings: { kinds: [...kinds, { category: "admin", off: false }], optionalEmailOff: false }, email: "a•••@example.com", fromCategory: "messages", canTurnOn: true });
  open("abc");
  expect(await screen.findByText(/You came here from a message from a leader/)).toBeInTheDocument();
  expect(await screen.findByRole("checkbox", { name: /^Messages from leaders/ })).toBeInTheDocument();
  expect(screen.getByRole("checkbox", { name: /^Admin notices/ })).toBeInTheDocument();
});
