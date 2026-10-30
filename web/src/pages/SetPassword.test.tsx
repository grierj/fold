import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { expect, it, vi } from "vitest";
import { SetPassword } from "./SetPassword";

vi.mock("../auth", () => ({ useAuth: () => ({ refresh: vi.fn() }) }));

it("gives the missing-token recovery link a phone-sized target", () => {
  render(<MemoryRouter initialEntries={["/set-password"]}><SetPassword /></MemoryRouter>);
  const link = screen.getByRole("link", { name: "Back to log in" });
  expect(link).toHaveAttribute("href", "/login");
  expect(link).toHaveClass("inline-flex", "min-h-11", "items-center");
  expect(link.parentElement?.className).not.toMatch(/leading-/);
});
