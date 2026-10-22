import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ChildcareRequestField, childcareCountProblem } from "./ChildcareRequestField";

it.each(["0", "20", ""])("accepts the child count %s", (value) => {
  expect(childcareCountProblem(value)).toBeNull();
});
it.each(["-1", "21", "1.5", "1e", "NaN"])("rejects the child count %s", (value) => {
  expect(childcareCountProblem(value)).toMatch(/whole number from 0 to 20/);
});
describe("ChildcareRequestField", () => {
  it("labels the count and ages and preserves typed text", async () => {
    const onCount = vi.fn();
    render(<ChildcareRequestField count="1e" ages="3 and 5" onCount={onCount} onAges={() => {}} error={childcareCountProblem("1e")} />);
    const count = screen.getByLabelText(/^Children needing childcare/);
    expect(screen.getByRole("group", { name: "Childcare for this date" })).toContainElement(count);
    expect(screen.getByRole("group", { name: "Childcare for this date" })).toContainElement(screen.getByLabelText(/^Ages/));
    expect(count).toHaveValue("1e");
    expect(count).toHaveAttribute("aria-invalid", "true");
    expect(count).toHaveAttribute("inputmode", "numeric");
    await userEvent.clear(count);
    expect(onCount).toHaveBeenCalledWith("");
    expect(screen.getByLabelText(/^Ages/)).toHaveValue("3 and 5");
    expect(screen.getByLabelText(/^Ages/)).toHaveAttribute("maxlength", "200");
    expect(screen.getByLabelText(/^Ages/)).toHaveAccessibleDescription("For example: 3 and 7. Only you and the event's leaders, hosts and admins can see this.");
  });

  it("disables the count and explains when childcare is full", () => {
    render(<ChildcareRequestField count="0" ages="" onCount={() => {}} onAges={() => {}} full />);
    expect(screen.getByLabelText(/^Children needing childcare/)).toBeDisabled();
    expect(screen.getByLabelText(/^Children needing childcare/)).toHaveAccessibleDescription("Childcare is full for this date");
    expect(screen.queryByText(/Leave it blank/)).not.toBeInTheDocument();
  });
});
