import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { DayButton } from "./DayButton";

describe("DayButton", () => {
  it("is a named toggle", async () => {
    const onClick = vi.fn();
    render(<DayButton label="Wednesday, October 21, 2 events" pressed onClick={onClick}>21</DayButton>);
    const b = screen.getByRole("button", { name: "Wednesday, October 21, 2 events" });
    expect(b).toHaveAttribute("aria-pressed", "true");
    await userEvent.click(b);
    expect(onClick).toHaveBeenCalled();
  });
});
