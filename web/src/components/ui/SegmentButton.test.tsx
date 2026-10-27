import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { SegmentButton } from "./SegmentButton";

it("is a pressed native button with the shared segment styling", () => {
  render(<SegmentButton pressed>Going</SegmentButton>);
  const button = screen.getByRole("button", { name: "Going" });
  expect(button).toHaveAttribute("type", "button");
  expect(button).toHaveAttribute("aria-pressed", "true");
  expect(button).toHaveClass("segment");
});

it.each(["Enter", " "])("activates from %s", async (key) => {
  const onClick = vi.fn();
  render(<SegmentButton pressed={false} onClick={onClick}>Maybe</SegmentButton>);
  const button = screen.getByRole("button", { name: "Maybe" });
  button.focus();
  await userEvent.keyboard(key === " " ? "[Space]" : `{${key}}`);
  expect(onClick).toHaveBeenCalledOnce();
});

it("does not activate while disabled", async () => {
  const onClick = vi.fn();
  render(<SegmentButton pressed={false} disabled onClick={onClick}>Can't go</SegmentButton>);
  const button = screen.getByRole("button", { name: "Can't go" });
  expect(button).toBeDisabled();
  await userEvent.click(button);
  expect(onClick).not.toHaveBeenCalled();
});
