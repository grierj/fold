import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { NewPasswordForm } from "./NewPasswordForm";

function setup(submit = vi.fn().mockResolvedValue(undefined)) {
  render(<NewPasswordForm idPrefix="t" passwordLabel="New password" confirmLabel="Confirm new password" submitLabel="Set it" submit={submit} describe={() => "Nope."} />);
  return submit;
}

describe("NewPasswordForm", () => {
  it("states the rule beside the field", () => {
    setup();
    expect(screen.getByLabelText("New password")).toHaveAccessibleDescription(/at least 10 characters, and at most 72 bytes/);
  });

  it("refuses a short password before asking the server", async () => {
    const submit = setup();
    await userEvent.type(screen.getByLabelText("New password"), "short");
    await userEvent.type(screen.getByLabelText("Confirm new password"), "short");
    await userEvent.click(screen.getByRole("button", { name: "Set it" }));
    expect(await screen.findByText("Password must be at least 10 characters.")).toBeInTheDocument();
    expect(submit).not.toHaveBeenCalled();
  });

  it("refuses passwords that differ", async () => {
    const submit = setup();
    await userEvent.type(screen.getByLabelText("New password"), "a long enough one");
    await userEvent.type(screen.getByLabelText("Confirm new password"), "a different one");
    await userEvent.click(screen.getByRole("button", { name: "Set it" }));
    expect(await screen.findByText("The passwords don't match.")).toBeInTheDocument();
    expect(submit).not.toHaveBeenCalled();
  });

  it("sends the password, and shows the failure in the page's words", async () => {
    const submit = setup(vi.fn().mockRejectedValue(new Error("x")));
    await userEvent.type(screen.getByLabelText("New password"), "a long enough one");
    await userEvent.type(screen.getByLabelText("Confirm new password"), "a long enough one");
    await userEvent.click(screen.getByRole("button", { name: "Set it" }));
    expect(submit).toHaveBeenCalledWith("a long enough one");
    expect(await screen.findByText("Nope.")).toBeInTheDocument();
  });
});
