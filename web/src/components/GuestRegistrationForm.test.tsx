import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { Guests } from "../api";
import { GuestRegistrationForm } from "./GuestRegistrationForm";

vi.mock("../api", async (actual) => ({
  ...(await actual<typeof import("../api")>()),
  Guests: { register: vi.fn() },
}));

it("requires the adult affirmation before requesting a guest confirmation", async () => {
  vi.mocked(Guests.register).mockResolvedValue({ message: "Check your email." });
  render(<GuestRegistrationForm instance={{ id: 51, eventId: 7, date: "2026-12-01" }} />);

  await userEvent.click(screen.getByRole("button", { name: "Register as a guest" }));
  const name = screen.getByRole("textbox", { name: "Full name" });
  const email = screen.getByRole("textbox", { name: "Email" });
  const age = screen.getByRole("checkbox", { name: /^I am 18 or older/ });
  const account = screen.getByRole("checkbox", { name: /^Also ask for a Fold account/ });
  await userEvent.type(name, "Synthetic Guest");
  await userEvent.type(email, "synthetic@example.com");

  await userEvent.click(screen.getByRole("button", { name: "Email me a confirmation link" }));

  expect(Guests.register).not.toHaveBeenCalled();
  expect(age).toBeRequired();
  expect(age).toHaveAttribute("aria-invalid", "true");
  const error = screen.getByText("Check this box to continue.");
  expect(error).toBeVisible();
  expect(age.getAttribute("aria-describedby")?.split(" ")).toContain(error.id);
  expect(age).toHaveFocus();
  expect(name).toHaveValue("Synthetic Guest");
  expect(email).toHaveValue("synthetic@example.com");
  expect(account).not.toBeRequired();

  await userEvent.click(age);
  await userEvent.click(screen.getByRole("button", { name: "Email me a confirmation link" }));

  await waitFor(() => expect(Guests.register).toHaveBeenCalledOnce());
  expect(Guests.register).toHaveBeenCalledWith(7, expect.objectContaining({
    over18: true,
    date: "2026-12-01",
    name: "Synthetic Guest",
    email: "synthetic@example.com",
  }));
});
