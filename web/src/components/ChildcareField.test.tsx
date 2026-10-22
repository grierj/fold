import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ChildcareField, childcareBody, childcareFrom, childcareProblem, emptyChildcare, focusChildcareCapacity } from "./ChildcareField";

describe("ChildcareField", () => {
  it("shows optional parent information only when childcare is offered", async () => {
    const change = vi.fn();
    const { rerender } = render(<ChildcareField idPrefix="test" value={emptyChildcare} onChange={change} />);
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("checkbox", { name: "Childcare available" }));
    expect(change).toHaveBeenCalledWith({ ...emptyChildcare, available: true });
    rerender(<ChildcareField idPrefix="test" value={childcareFrom(true, 5, "2–5", "Nursery")} onChange={change} />);
    expect(screen.getByLabelText(/^Childcare limit/)).toHaveValue("5");
    expect(screen.getByLabelText(/^Ages/)).toHaveValue("2–5");
    expect(screen.getByLabelText(/^Notes for parents/)).toHaveValue("Nursery");
    expect(screen.getByLabelText(/^Ages/)).toHaveAttribute("maxlength", "100");
    expect(screen.getByLabelText(/^Notes for parents/)).toHaveAttribute("maxlength", "500");
  });

  it.each(["0", "-1", "1.5", "1e", "100001"])("keeps invalid text %s and focuses the field with its error", async (capacity) => {
    const value = { ...emptyChildcare, available: true, capacity };
    const error = childcareProblem(value);
    expect(error).not.toBeNull();
    render(<ChildcareField idPrefix="bad" value={value} onChange={() => {}} error={error} />);
    const input = screen.getByRole("textbox", { name: /^Childcare limit/ });
    expect(input).toHaveValue(capacity);
    expect(input).toHaveAttribute("inputmode", "numeric");
    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(input).toHaveAccessibleDescription(expect.stringContaining(error ?? ""));
    focusChildcareCapacity("bad");
    await waitFor(() => expect(input).toHaveFocus());
  });

  it("allows an empty limit, maps the fields and ignores the off limit", () => {
    expect(childcareProblem(childcareFrom(true, null))).toBeNull();
    expect(childcareProblem({ ...emptyChildcare, capacity: "0" })).toBeNull();
    expect(childcareBody(childcareFrom(true, 4, "2–5", "Nursery"))).toEqual({
      childcareAvailable: true, childcareCapacity: 4, childcareAges: "2–5", childcareNotes: "Nursery",
    });
    expect(childcareBody(childcareFrom(true, null)).childcareCapacity).toBeNull();
    expect(childcareBody(childcareFrom(false, 4)).childcareCapacity).toBeNull();
  });
});
