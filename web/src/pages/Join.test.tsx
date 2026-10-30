import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { expect, it, vi } from "vitest";
import { Join } from "./Join";

vi.mock("../api", () => ({
  Site: { get: vi.fn().mockResolvedValue({ name: "Grace", selfSignup: "off" }) },
}));

it("keeps the login link phone-sized without enlarging the surrounding prose", async () => {
  render(<MemoryRouter><Join /></MemoryRouter>);
  await screen.findByRole("heading", { name: "Request an account at Grace" });
  expect(screen.getByText("Send your request.")).toBeInTheDocument();
  expect(screen.getByText("An admin reviews it.")).toBeInTheDocument();
  expect(screen.getByText("We email you a link to set your password.")).toBeInTheDocument();
  const link = screen.getByRole("link", { name: "Log in" });
  expect(link).toHaveAttribute("href", "/login");
  expect(link).toHaveClass("text-link");
  expect(link.parentElement?.className).not.toMatch(/leading-/);
});
