import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";

const guardianCopies = vi.fn();
const setGuardianCopies = vi.fn();
const replyTo = vi.fn();
const setReplyTo = vi.fn();
vi.mock("../../api", async (orig) => {
  const real = await orig<typeof import("../../api")>();
  return {
    ...real,
    Admin: {
      ...real.Admin,
      guardianCopies: () => guardianCopies(), setGuardianCopies: (on: boolean) => setGuardianCopies(on),
      replyTo: () => replyTo(), setReplyTo: (a: string) => setReplyTo(a),
    },
  };
});

import { ApiError } from "../../api";
import { ERROR_COPY } from "../../lib/errors";
import { GuardianCopiesSetting, ReplyToSetting } from "./MessageSettings";

beforeEach(() => {
  guardianCopies.mockReset().mockResolvedValue({ on: false });
  setGuardianCopies.mockReset().mockImplementation(async (on: boolean) => ({ on }));
  replyTo.mockReset().mockResolvedValue({ address: "" });
  setReplyTo.mockReset().mockImplementation(async (a: string) => ({ address: a }));
});

const copies = () => screen.findByRole("checkbox", { name: /Send guardians a copy of messages to their teens/ });

it("turns guardian copies on at once and says Saved", async () => {
  render(<MemoryRouter><GuardianCopiesSetting /></MemoryRouter>);
  const box = await copies();
  expect(box).not.toBeChecked();
  expect(screen.getByText("Each confirmed guardian gets an email copy, marked as a copy. Off by default.")).toBeInTheDocument();
  await userEvent.click(box);
  expect(setGuardianCopies).toHaveBeenCalledWith(true);
  expect(await screen.findByRole("status")).toHaveTextContent("Saved");
  expect(box).toBeChecked();
});

it("goes back when guardian copies can't be saved", async () => {
  guardianCopies.mockResolvedValue({ on: true });
  setGuardianCopies.mockRejectedValue(new ApiError(500, "server words"));
  render(<MemoryRouter><GuardianCopiesSetting /></MemoryRouter>);
  const box = await copies();
  await userEvent.click(box);
  expect(await screen.findByRole("alert")).toHaveTextContent("We couldn't save that setting.");
  expect(box).toBeChecked();
  expect(screen.queryByText("server words")).not.toBeInTheDocument();
});

const replyBox = () => screen.findByRole("textbox", { name: /Where replies go/ });

it("saves where replies go", async () => {
  render(<MemoryRouter><ReplyToSetting /></MemoryRouter>);
  const box = await replyBox();
  expect(box).toHaveAccessibleDescription(/Replies to announcements go here, usually the church office. Leave empty for no reply address./);
  const save = screen.getByRole("button", { name: "Save reply address" });
  expect(save).toBeDisabled();
  await userEvent.type(box, " office@grace.org ");
  await userEvent.click(save);
  expect(setReplyTo).toHaveBeenCalledWith("office@grace.org");
  expect(await screen.findByRole("status")).toHaveTextContent("Saved");
});

it("says under the field when the server refuses the address", async () => {
  setReplyTo.mockRejectedValue(new ApiError(400, "server words", { code: "reply_to_invalid" }));
  render(<MemoryRouter><ReplyToSetting /></MemoryRouter>);
  await userEvent.type(await replyBox(), "office@grace");
  await userEvent.click(screen.getByRole("button", { name: "Save reply address" }));
  expect(await replyBox()).toHaveAccessibleDescription(expect.stringContaining(ERROR_COPY.reply_to_invalid));
  expect(screen.queryByText("server words")).not.toBeInTheDocument();
});

it("checks the address's shape before sending it", async () => {
  render(<MemoryRouter><ReplyToSetting /></MemoryRouter>);
  await userEvent.type(await replyBox(), "the office");
  await userEvent.click(screen.getByRole("button", { name: "Save reply address" }));
  expect(await replyBox()).toHaveAccessibleDescription(expect.stringContaining("Enter a valid email address."));
  expect(setReplyTo).not.toHaveBeenCalled();
});

it("clears where replies go", async () => {
  replyTo.mockResolvedValue({ address: "office@grace.org" });
  render(<MemoryRouter><ReplyToSetting /></MemoryRouter>);
  const box = await replyBox();
  expect(box).toHaveValue("office@grace.org");
  await userEvent.clear(box);
  await userEvent.click(screen.getByRole("button", { name: "Save reply address" }));
  expect(setReplyTo).toHaveBeenCalledWith("");
});
