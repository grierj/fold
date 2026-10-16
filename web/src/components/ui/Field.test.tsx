import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { Field, Input, Select } from "./Field";

describe("Field (ENFORCEMENT C5)", () => {
  it("labels its control and lists the hint, then the error", async () => {
    render(<Field label="Email" hint="We never share it." error="That email isn't valid."><Input /></Field>);
    const input = screen.getByLabelText("Email");
    const [hint, error] = input.getAttribute("aria-describedby")!.split(" ");
    expect(document.getElementById(hint)).toHaveTextContent("We never share it.");
    expect(document.getElementById(error)).toHaveTextContent("That email isn't valid.");
    expect(input).toHaveAttribute("aria-invalid", "true");
    await userEvent.click(screen.getByText("Email"));
    expect(input).toHaveFocus();
  });
  it("refuses a control outside a Field", () => {
    expect(() => render(<Input />)).toThrow(/inside a <Field/);
  });
  it("titles a select with its chosen label", () => {
    render(<Field label="Zone"><Select value="b" onChange={() => {}} options={[{ value: "a", label: "A" }, { value: "b", label: "America/Los_Angeles" }]} /></Field>);
    expect(screen.getByLabelText("Zone")).toHaveAttribute("title", "America/Los_Angeles");
  });
});
