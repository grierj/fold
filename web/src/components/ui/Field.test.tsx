import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { Checkbox, Choices, Field, focusFirstInvalid, Input, Radio, Select } from "./Field";

describe("Field (ENFORCEMENT C5, DEC-65)", () => {
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
  it("puts the hint under the label and above the control, and the error under it", () => {
    render(<Field label="Email" hint="Hint" error="Oops"><Input /></Field>);
    const order = [screen.getByText("Email"), screen.getByText("Hint"), screen.getByLabelText("Email"), screen.getByText("Oops")];
    for (let i = 1; i < order.length; i++) {
      expect(order[i - 1].compareDocumentPosition(order[i]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    }
  });
  it("marks only optional fields, without an asterisk", () => {
    render(<><Field label="Phone" optional><Input /></Field><Field label="Name"><Input /></Field></>);
    expect(screen.getByLabelText("Phone (optional)")).toBeInTheDocument();
    expect(screen.getByLabelText("Name")).toBeInTheDocument();
    expect(document.body.textContent).not.toContain("*");
  });
  it("keeps a fixed id when given one", () => {
    render(<Field id="email" label="Email"><Input /></Field>);
    expect(screen.getByLabelText("Email")).toHaveAttribute("id", "email");
  });
  it("refuses a control outside a Field", () => {
    expect(() => render(<Input />)).toThrow(/inside a <Field/);
  });
  it("titles a select with its chosen label", () => {
    render(<Field label="Zone"><Select value="b" onChange={() => {}} options={[{ value: "a", label: "A" }, { value: "b", label: "America/Los_Angeles" }]} /></Field>);
    expect(screen.getByLabelText("Zone")).toHaveAttribute("title", "America/Los_Angeles");
  });
});

describe("Choices, Radio and Checkbox rows", () => {
  it("groups radios under a legend, and the whole row is the label", async () => {
    let picked = "";
    render(
      <Choices legend="Their age" hint="Pick one.">
        <Radio name="age" label="18 or older" description="An adult account" onChange={() => (picked = "adult")} />
        <Radio name="age" label="13 to 17" onChange={() => (picked = "teen")} />
      </Choices>,
    );
    const group = screen.getByRole("group", { name: "Their age" });
    expect(group).toHaveAccessibleDescription("Pick one.");
    await userEvent.click(screen.getByText("An adult account"));
    expect(picked).toBe("adult");
    expect(screen.getByRole("radio", { name: /^18 or older/ })).toBeChecked();
  });
  it("labels a checkbox by its row", () => {
    render(<Checkbox label="Repeat weekly" defaultChecked />);
    expect(screen.getByRole("checkbox", { name: "Repeat weekly" })).toBeChecked();
  });
});

describe("focusFirstInvalid", () => {
  it("focuses the first field marked invalid, or a fieldset's first control", () => {
    const { container, rerender } = render(
      <form>
        <Field label="A"><Input /></Field>
        <Field label="B" error="Wrong"><Input /></Field>
        <Field label="C" error="Wrong too"><Input /></Field>
      </form>,
    );
    expect(focusFirstInvalid(container.querySelector("form"))).toBe(true);
    expect(screen.getByLabelText("B")).toHaveFocus();
    rerender(
      <form>
        <Choices legend="Age" error="Pick one"><Radio name="a" label="Adult" /></Choices>
      </form>,
    );
    focusFirstInvalid(container.querySelector("form"));
    expect(screen.getByRole("radio", { name: "Adult" })).toHaveFocus();
    rerender(<form><Field label="D"><Input /></Field></form>);
    expect(focusFirstInvalid(container.querySelector("form"))).toBe(false);
  });
});
