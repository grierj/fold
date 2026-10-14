import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { AccountRequestForm } from "./AccountRequestForm";

describe("AccountRequestForm age gate (M1.3, DEC-8)", () => {
  it("under 13 shows the household explanation and offers no submit", async () => {
    const onSubmit = vi.fn(async () => ({ message: "ok" }));
    render(<AccountRequestForm submitLabel="Request an account" onSubmit={onSubmit} />);
    await userEvent.click(screen.getByLabelText("Under 13"));
    expect(screen.getByText(/accounts start at 13/i)).toBeInTheDocument();
    expect(screen.getByText(/add you to their household/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /request an account/i })).not.toBeInTheDocument();
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("13 to 17 asks for a birth date and submits it", async () => {
    const onSubmit = vi.fn(async () => ({ message: "Thanks — check your email for next steps." }));
    render(<AccountRequestForm submitLabel="Request an account" onSubmit={onSubmit} />);
    await userEvent.type(screen.getByLabelText("Full name"), "Teen Tess");
    await userEvent.type(screen.getByLabelText("Email"), "tess@example.com");
    await userEvent.click(screen.getByLabelText("13 to 17"));
    const birth = screen.getByLabelText("Birth date");
    await userEvent.type(birth, "2011-06-15");
    await userEvent.click(screen.getByRole("button", { name: /request an account/i }));
    expect(onSubmit).toHaveBeenCalledWith(
      expect.objectContaining({ ageBand: "teen", birthDate: "2011-06-15", email: "tess@example.com" })
    );
    expect(await screen.findByText(/check your email/i)).toBeInTheDocument();
  });

  it("speaks about the friend when a member invites someone else", () => {
    const onSubmit = vi.fn(async () => ({ message: "ok" }));
    render(<AccountRequestForm submitLabel="Ask an admin to invite them" subject="friend" onSubmit={onSubmit} />);
    expect(screen.getByText("This is the address they will sign in with.")).toBeInTheDocument();
    expect(screen.getByText("Their age")).toBeInTheDocument();
    expect(screen.queryByText("This is the address you will sign in with.")).not.toBeInTheDocument();
  });

  it("adults submit without a birth date", async () => {
    const onSubmit = vi.fn(async () => ({ message: "Thanks — check your email for next steps." }));
    render(<AccountRequestForm submitLabel="Request an account" onSubmit={onSubmit} />);
    await userEvent.type(screen.getByLabelText("Full name"), "Adult Ann");
    await userEvent.type(screen.getByLabelText("Email"), "ann@example.com");
    expect(screen.queryByLabelText("Birth date")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /request an account/i }));
    expect(onSubmit).toHaveBeenCalledWith(
      expect.objectContaining({ ageBand: "adult", birthDate: undefined })
    );
  });
});
