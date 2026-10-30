import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { expect, it, vi } from "vitest";
import { ResetPassword } from "./ResetPassword";

vi.mock("../auth", () => ({ useAuth: () => ({ refresh: vi.fn() }) }));

it("gives the missing-token recovery link a phone-sized target", () => {
  render(<MemoryRouter initialEntries={["/reset-password"]}><ResetPassword /></MemoryRouter>);
  const link = screen.getByRole("link", { name: "Request a new reset link" });
  expect(link).toHaveAttribute("href", "/forgot-password");
  expect(link).toHaveClass("inline-flex", "min-h-11", "items-center");
  expect(link.parentElement?.className).not.toMatch(/leading-/);
});
