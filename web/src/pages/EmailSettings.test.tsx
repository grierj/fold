import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import type { EmailSettingsChange } from "../api";

const get = vi.fn();
const update = vi.fn();
vi.mock("../api", async (orig) => ({
  ...(await orig<typeof import("../api")>()),
  EmailSettingsLink: { get: (t: string) => get(t), update: (t: string, c: EmailSettingsChange) => update(t, c) },
}));

import { ApiError } from "../api";
import { EmailSettingsPage } from "./EmailSettings";

const kinds = [
  { category: "groups" as const, off: false },
  { category: "events" as const, off: false },
  { category: "messages" as const, off: false },
];

beforeEach(() => {
  get.mockReset().mockResolvedValue({ emailSettings: { kinds, optionalEmailOff: false }, email: "s•••@example.com", fromCategory: "events" });
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
