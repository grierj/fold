import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { Menu } from "./Menu";

function setup() {
  const edit = vi.fn(), del = vi.fn();
  render(
    <>
      <Menu label="More for Sam" items={[
        { label: "Edit", onSelect: edit },
        { label: "Archive", onSelect: () => {}, disabled: true },
        { label: "Delete", onSelect: del, danger: true },
      ]} />
      <button type="button">Elsewhere</button>
    </>,
  );
  return { edit, del, trigger: screen.getByRole("button", { name: "More for Sam" }) };
}

describe("Menu", () => {
  it("says whether it's open and moves focus into the menu", () => {
    const { trigger } = setup();
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    expect(trigger).toHaveAttribute("aria-haspopup", "menu");
    expect(trigger).toHaveTextContent("More");
    fireEvent.click(trigger);
    expect(trigger).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("menu", { name: "More for Sam" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Edit" })).toHaveFocus();
  });
  it("arrows skip disabled items and wrap; Escape returns focus to the trigger", () => {
    const { trigger } = setup();
    fireEvent.click(trigger);
    fireEvent.keyDown(document.activeElement!, { key: "ArrowDown" });
    expect(screen.getByRole("menuitem", { name: "Delete" })).toHaveFocus();
    fireEvent.keyDown(document.activeElement!, { key: "ArrowDown" });
    expect(screen.getByRole("menuitem", { name: "Edit" })).toHaveFocus();
    fireEvent.keyDown(document.activeElement!, { key: "End" });
    expect(screen.getByRole("menuitem", { name: "Delete" })).toHaveFocus();
    fireEvent.keyDown(document.activeElement!, { key: "Escape" });
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });
  it("runs the chosen item, closes, and closes on an outside click", () => {
    const { trigger, del } = setup();
    fireEvent.keyDown(trigger, { key: "ArrowDown" });
    fireEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
    expect(del).toHaveBeenCalledOnce();
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
    fireEvent.click(trigger);
    fireEvent.pointerDown(screen.getByRole("button", { name: "Elsewhere" }));
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });
  it("items are 44px targets and danger items read as danger", () => {
    const { trigger } = setup();
    fireEvent.click(trigger);
    expect(screen.getByRole("menuitem", { name: "Edit" })).toHaveClass("min-h-11");
    expect(screen.getByRole("menuitem", { name: "Delete" })).toHaveClass("text-danger");
    expect(screen.getByRole("menuitem", { name: "Archive" })).toBeDisabled();
  });
});
