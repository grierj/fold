import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Chip } from "./Chip";

describe("Chip", () => {
  it("says whether it is selected with aria-pressed, never by colour alone", () => {
    render(<><Chip selected>bible-study</Chip><Chip selected={false}>youth</Chip></>);
    expect(screen.getByRole("button", { name: "bible-study" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "youth" })).toHaveAttribute("aria-pressed", "false");
  });
  it("is a button that does not submit a form and passes clicks through", async () => {
    const onClick = vi.fn();
    render(<Chip selected={false} onClick={onClick}>youth</Chip>);
    expect(screen.getByRole("button", { name: "youth" })).toHaveAttribute("type", "button");
    await userEvent.click(screen.getByRole("button", { name: "youth" }));
    expect(onClick).toHaveBeenCalledTimes(1);
  });
});
