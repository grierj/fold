import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";

const get = vi.fn();
const update = vi.fn();
vi.mock("../api", async (orig) => ({
  ...(await orig<typeof import("../api")>()),
  NotificationSettings: { get: () => get(), update: (off: boolean) => update(off) },
}));

import { ApiError } from "../api";
import { NotificationSettingsCard } from "./NotificationSettingsCard";

const label = "Turn off all optional email";

beforeEach(() => {
  get.mockReset().mockResolvedValue({ settings: { optionalEmailOff: false } });
  update.mockReset().mockImplementation(async (off: boolean) => ({ settings: { optionalEmailOff: off } }));
});

it("shows a skeleton line while loading, then the switch as the server has it", async () => {
  let finish: (v: unknown) => void = () => {};
  get.mockReturnValue(new Promise((resolve) => { finish = resolve; }));
  render(<NotificationSettingsCard />);
  expect(screen.getByText("Loading your email settings…")).toBeInTheDocument();
  expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  await act(async () => { finish({ settings: { optionalEmailOff: true } }); });
  expect(await screen.findByRole("checkbox", { name: new RegExp(`^${label}`) })).toBeChecked();
  expect(screen.getByText(/You'll still get emails about your account, safety, and requests waiting on you/)).toBeInTheDocument();
  expect(screen.getByText(/In-app alerts are always on/)).toBeInTheDocument();
});

it("saves on change with no Save button and says Saved", async () => {
  render(<NotificationSettingsCard />);
  const box = await screen.findByRole("checkbox", { name: new RegExp(`^${label}`) });
  expect(box).not.toBeChecked();
  expect(screen.queryByRole("button")).not.toBeInTheDocument();
  await userEvent.click(box);
  expect(update).toHaveBeenCalledWith(true);
  expect(await screen.findByText("Saved")).toBeInTheDocument();
  expect(screen.getByRole("status")).toHaveTextContent("Saved");
  expect(box).toBeChecked();
});

it("disables the switch while it saves, so saves can't overlap", async () => {
  let finish: (v: unknown) => void = () => {};
  update.mockReturnValue(new Promise((resolve) => { finish = resolve; }));
  render(<NotificationSettingsCard />);
  const box = await screen.findByRole("checkbox", { name: new RegExp(`^${label}`) });
  await userEvent.click(box);
  expect(box).toBeDisabled();
  await userEvent.click(box);
  expect(update).toHaveBeenCalledOnce();
  await act(async () => { finish({ settings: { optionalEmailOff: true } }); });
  expect(await screen.findByText("Saved")).toHaveClass("text-success");
  expect(box).toBeEnabled();
});

it("puts the box back and says what went wrong when saving fails, without server text", async () => {
  update.mockRejectedValueOnce(new ApiError(500, "private server text"));
  render(<NotificationSettingsCard />);
  const box = await screen.findByRole("checkbox", { name: new RegExp(`^${label}`) });
  await userEvent.click(box);
  expect(await screen.findByRole("alert")).toHaveTextContent("We couldn't save your changes.");
  expect(box).not.toBeChecked();
  expect(box).toBeEnabled();
  expect(screen.queryByText("Saved")).not.toBeInTheDocument();
  expect(screen.queryByText("private server text")).not.toBeInTheDocument();
});

it("offers a retry when the first load fails, then shows the switch", async () => {
  get.mockRejectedValueOnce(new ApiError(500, "private server text"));
  render(<NotificationSettingsCard />);
  expect(await screen.findByRole("alert")).toHaveTextContent("We couldn't load your email settings.");
  expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  expect(screen.queryByText("private server text")).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Try again" }));
  expect(await screen.findByRole("checkbox", { name: new RegExp(`^${label}`) })).toBeInTheDocument();
});
