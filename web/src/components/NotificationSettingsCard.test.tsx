import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import type { EmailSettingsChange } from "../api";
import type { EmailKind } from "../types";

const get = vi.fn();
const update = vi.fn();
vi.mock("../api", async (orig) => ({
  ...(await orig<typeof import("../api")>()),
  NotificationSettings: { get: () => get(), update: (c: EmailSettingsChange) => update(c) },
}));

import { ApiError } from "../api";
import { NotificationSettingsCard } from "./NotificationSettingsCard";

const all = "Turn off all optional email";
const kinds = (off: Partial<Record<EmailKind["category"], boolean>> = {}): EmailKind[] =>
  (["groups", "events", "messages"] as const).map((category) => ({ category, off: !!off[category] }));
const settings = (k: EmailKind[]) => ({ settings: { kinds: k, optionalEmailOff: k.every((x) => x.off) } });

// The server applies a change the way the real one does.
let state = kinds();
beforeEach(() => {
  state = kinds();
  get.mockReset().mockImplementation(async () => settings(state));
  update.mockReset().mockImplementation(async (c: EmailSettingsChange) => {
    state = state.map((k) => (!c.category || k.category === c.category ? { ...k, off: c.off } : k));
    return settings(state);
  });
});

it("shows a skeleton while loading, then a switch per kind and the all-off switch", async () => {
  let finish: (v: unknown) => void = () => {};
  get.mockReturnValue(new Promise((resolve) => { finish = resolve; }));
  render(<NotificationSettingsCard />);
  expect(screen.getByText("Loading your email settings…")).toBeInTheDocument();
  await act(async () => { finish(settings(kinds({ events: true }))); });
  expect(await screen.findByRole("checkbox", { name: /^Group updates/ })).toBeChecked();
  expect(screen.getByRole("checkbox", { name: /^Event updates/ })).not.toBeChecked();
  expect(screen.getByRole("checkbox", { name: /^Messages from leaders/ })).toBeChecked();
  expect(screen.queryByRole("checkbox", { name: /^Admin notices/ })).not.toBeInTheDocument();
  expect(screen.getByRole("checkbox", { name: new RegExp(`^${all}`) })).not.toBeChecked();
  expect(screen.getByText(/Account and safety email always comes/)).toBeInTheDocument();
  expect(screen.getByText("Check this to stop every kind above. Uncheck it to turn them all back on.")).toBeInTheDocument();
});

it("saves one kind on change with no Save button and says Saved", async () => {
  render(<NotificationSettingsCard />);
  const box = await screen.findByRole("checkbox", { name: /^Group updates/ });
  expect(screen.queryByRole("button")).not.toBeInTheDocument();
  await userEvent.click(box);
  expect(update).toHaveBeenCalledWith({ category: "groups", off: true });
  expect(await screen.findByText("Saved")).toBeInTheDocument();
  expect(screen.getByRole("status")).toHaveTextContent("Saved");
  expect(box).not.toBeChecked();
});

it("turns every kind off at once, and the all-off switch follows the kinds", async () => {
  render(<NotificationSettingsCard />);
  const allBox = await screen.findByRole("checkbox", { name: new RegExp(`^${all}`) });
  await userEvent.click(allBox);
  expect(update).toHaveBeenCalledWith({ off: true });
  expect(await screen.findByText("Saved")).toBeInTheDocument();
  for (const label of [/^Group updates/, /^Event updates/, /^Messages from leaders/]) {
    expect(screen.getByRole("checkbox", { name: label })).not.toBeChecked();
  }
  expect(allBox).toBeChecked();
  // Turning one kind back on means not everything is off.
  await userEvent.click(screen.getByRole("checkbox", { name: /^Event updates/ }));
  expect(await screen.findByRole("checkbox", { name: new RegExp(`^${all}`) })).not.toBeChecked();
});

it("disables the switches while one saves, so saves can't overlap", async () => {
  let finish: (v: unknown) => void = () => {};
  update.mockReturnValue(new Promise((resolve) => { finish = resolve; }));
  render(<NotificationSettingsCard />);
  const box = await screen.findByRole("checkbox", { name: /^Group updates/ });
  await userEvent.click(box);
  // aria-disabled, not disabled, so keyboard focus stays on the switch.
  expect(box).toHaveAttribute("aria-disabled", "true");
  expect(box).toHaveFocus();
  expect(screen.getByRole("checkbox", { name: /^Event updates/ })).toHaveAttribute("aria-disabled", "true");
  await userEvent.click(box);
  await userEvent.click(screen.getByRole("checkbox", { name: /^Event updates/ }));
  expect(update).toHaveBeenCalledOnce();
  await act(async () => { finish(settings(kinds({ groups: true }))); });
  expect(await screen.findByText("Saved")).toHaveClass("text-success");
  expect(box).not.toHaveAttribute("aria-disabled");
});

it("puts the switches back and says what went wrong when saving fails, without server text", async () => {
  update.mockRejectedValueOnce(new ApiError(500, "private server text"));
  render(<NotificationSettingsCard />);
  const box = await screen.findByRole("checkbox", { name: /^Group updates/ });
  await userEvent.click(box);
  expect(await screen.findByRole("alert")).toHaveTextContent("We couldn't save your changes. Try again.");
  expect(box).toBeChecked();
  expect(box).not.toHaveAttribute("aria-disabled");
  expect(screen.queryByText("Saved")).not.toBeInTheDocument();
  expect(screen.queryByText("private server text")).not.toBeInTheDocument();
});

it("offers a retry when the first load fails, then shows the switches", async () => {
  get.mockRejectedValueOnce(new ApiError(500, "private server text"));
  render(<NotificationSettingsCard />);
  expect(await screen.findByRole("alert")).toHaveTextContent("We couldn't load your email settings.");
  expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  expect(screen.queryByText("private server text")).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Try again" }));
  const group = await screen.findByRole("group", { name: /^Optional email/ });
  expect(within(group).getByRole("checkbox", { name: /^Group updates/ })).toBeInTheDocument();
});

it("lists admin notices for admins", async () => {
  state = [...kinds(), { category: "admin", off: false }];
  render(<NotificationSettingsCard />);
  expect(await screen.findByRole("checkbox", { name: /^Admin notices/ })).toBeInTheDocument();
  expect(screen.getByText("For church admins")).toBeInTheDocument();
});
