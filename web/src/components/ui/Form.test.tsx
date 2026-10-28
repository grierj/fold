import { createRef, type FormEvent } from "react";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Button } from "./Button";
import { Checkbox, Choices, Field, Input, Radio, Select } from "./Field";
import { Form } from "./Form";

const submission = () => vi.fn((event: FormEvent<HTMLFormElement>) => event.preventDefault());

describe("Form", () => {
  it("shows every required error, preserves values and hints, and focuses the first field", async () => {
    const submit = submission();
    const ref = createRef<HTMLFormElement>();
    render(<Form ref={ref} noValidate={undefined} onSubmit={submit}>
      <Field label="Group name" hint="Use a name people will recognize."><Input defaultValue="   " required /></Field>
      <Field label="Email"><Input type="email" required /></Field>
      <Button variant="primary" type="submit">Save</Button>
    </Form>);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    const name = screen.getByRole("textbox", { name: "Group name" });
    const email = screen.getByRole("textbox", { name: "Email" });
    expect(ref.current).toHaveAttribute("novalidate");
    expect(submit).not.toHaveBeenCalled();
    expect(name).toHaveValue("   ");
    expect(name).toHaveAttribute("aria-invalid", "true");
    expect(email).toHaveAttribute("aria-invalid", "true");
    expect(name).toHaveAccessibleDescription("Use a name people will recognize. Enter a group name.");
    expect(email).toHaveAccessibleDescription("Enter an email address.");
    expect(name).toHaveFocus();
  });

  it("keeps errors through scrolling and invalid edits, and clears them only when corrected", async () => {
    const submit = submission();
    render(<Form onSubmit={submit}>
      <Field label="Email"><Input type="email" required /></Field>
      <Button variant="primary" type="submit">Save</Button>
    </Form>);
    const email = screen.getByRole("textbox", { name: "Email" });
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await userEvent.type(email, "bad");
    fireEvent.scroll(window);
    expect(email).toHaveAttribute("aria-invalid", "true");
    expect(email).toHaveAccessibleDescription("Enter a valid email address.");
    expect(submit).not.toHaveBeenCalled();
    await userEvent.clear(email);
    await userEvent.type(email, "member@example.com");
    expect(email).not.toHaveAttribute("aria-invalid");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(submit).toHaveBeenCalledOnce();
  });

  it("wires required selects, checkbox rows and radio groups to persistent errors", async () => {
    const submit = submission();
    render(<Form onSubmit={submit}>
      <Field label="Group"><Select value="" onChange={() => {}} required options={[{ value: "", label: "Choose a group" }, { value: "1", label: "Study" }]} /></Field>
      <Checkbox label="I agree" required />
      <Choices legend="Attendance">
        <Radio name="attendance" label="In person" value="in_person" required />
        <Radio name="attendance" label="Remote" value="remote" required />
      </Choices>
      <Button variant="primary" type="submit">Save</Button>
    </Form>);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    const group = screen.getByRole("combobox", { name: "Group" });
    const agreement = screen.getByRole("checkbox", { name: "I agree" });
    const attendance = screen.getByRole("group", { name: "Attendance" });
    expect(group).toHaveAccessibleDescription("Choose an option.");
    expect(group).toHaveFocus();
    expect(agreement).toHaveAttribute("aria-invalid", "true");
    expect(agreement).toHaveAccessibleDescription("Check this box to continue.");
    expect(attendance).toHaveAttribute("aria-invalid", "true");
    expect(attendance).toHaveAccessibleDescription("Choose an option.");
    await userEvent.click(agreement);
    await userEvent.click(screen.getByRole("radio", { name: "Remote" }));
    expect(agreement).not.toHaveAttribute("aria-invalid");
    expect(attendance).not.toHaveAttribute("aria-invalid");
    expect(group).toHaveAttribute("aria-invalid", "true");
    expect(submit).not.toHaveBeenCalled();
  });

  it("keeps optional, disabled and read-only constraint semantics", async () => {
    const submit = submission();
    render(<Form onSubmit={submit}>
      <Field label="Optional name" optional><Input minLength={5} /></Field>
      <Field label="Disabled"><Input required disabled /></Field>
      <Field label="Read-only"><Input required readOnly /></Field>
      <Button variant="primary" type="submit">Save</Button>
    </Form>);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(submit).toHaveBeenCalledOnce();
    expect(document.querySelector('[aria-invalid="true"]')).toBeNull();
  });

  it.each([" ".repeat(10), "  secret with spaces  "])("never trims or rejects meaningful password whitespace (%j)", async (password) => {
    let received = "";
    const submit = vi.fn((event: FormEvent<HTMLFormElement>) => {
      event.preventDefault();
      received = within(event.currentTarget).getByLabelText<HTMLInputElement>("Password").value;
    });
    render(<Form onSubmit={submit}>
      <Field id="constructor" label="Password"><Input type="password" defaultValue={password} minLength={10} required /></Field>
      <Button variant="primary" type="submit">Save</Button>
    </Form>);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(submit).toHaveBeenCalledOnce();
    expect(received).toBe(password);
  });

  it("retains native programmatic-length semantics instead of imposing a new code-unit limit", async () => {
    const submit = submission();
    render(<Form onSubmit={submit}>
      <Field label="Name"><Input defaultValue={"🌿".repeat(3)} maxLength={3} required /></Field>
      <Button variant="primary" type="submit">Save</Button>
    </Form>);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(submit).toHaveBeenCalledOnce();
  });

  it("keeps numeric bounds and whole-number constraints without browser bubbles", async () => {
    const submit = submission();
    render(<Form onSubmit={submit}>
      <Field label="Seats"><Input type="number" min={1} max={5} defaultValue="0" required /></Field>
      <Button variant="primary" type="submit">Save</Button>
    </Form>);
    const seats = screen.getByRole("spinbutton", { name: "Seats" });
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(seats).toHaveAccessibleDescription("Enter 1 or more.");
    await userEvent.clear(seats);
    await userEvent.type(seats, "1.5");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(seats).toHaveAccessibleDescription("Enter a whole number.");
    await userEvent.clear(seats);
    await userEvent.type(seats, "2");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(submit).toHaveBeenCalledOnce();
  });
});
