import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";

const pushEnabled = vi.fn();
const setPushEnabled = vi.fn();
vi.mock("../../api", async (orig) => {
  const real = await orig<typeof import("../../api")>();
  return { ...real, Admin: { ...real.Admin, pushEnabled: () => pushEnabled(), setPushEnabled: (on: boolean) => setPushEnabled(on) } };
});

import { ApiError } from "../../api";
import { PushEnabledSetting } from "./PushEnabledSetting";

beforeEach(() => {
  pushEnabled.mockReset().mockResolvedValue({ on: true });
  setPushEnabled.mockReset().mockImplementation(async (on: boolean) => ({ on }));
});

const box = () => screen.findByRole("checkbox", { name: /^Notifications on devices/ });

it("is on by default and turns off at once, saying Saved", async () => {
  render(<PushEnabledSetting />);
  const b = await box();
  expect(b).toBeChecked();
  expect(screen.getByText("Members can turn on notifications on their phones and computers. Turn this off to stop all of them; email and Alerts still work. Members' devices stay set up for when you turn it back on.")).toBeInTheDocument();
  await userEvent.click(b);
  expect(setPushEnabled).toHaveBeenCalledWith(false);
  expect(await screen.findByRole("status")).toHaveTextContent("Saved");
  expect(b).not.toBeChecked();
});

it("goes back and says why when saving fails, without the server's words", async () => {
  setPushEnabled.mockRejectedValue(new ApiError(500, "server words"));
  render(<PushEnabledSetting />);
  const b = await box();
  await userEvent.click(b);
  expect(await screen.findByRole("alert")).toHaveTextContent("We couldn't save that setting.");
  expect(b).toBeChecked();
  expect(screen.queryByText("server words")).not.toBeInTheDocument();
});

it("offers a retry when it can't load", async () => {
  pushEnabled.mockRejectedValueOnce(new ApiError(500, "x"));
  render(<PushEnabledSetting />);
  await userEvent.click(await screen.findByRole("button", { name: "Try again" }));
  expect(await box()).toBeChecked();
});
