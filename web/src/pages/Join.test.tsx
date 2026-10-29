import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { expect, it, vi } from "vitest";
import { Join } from "./Join";

vi.mock("../api", () => ({
  Site: { get: vi.fn().mockResolvedValue({ name: "Grace", selfSignup: "off" }) },
}));

it("keeps the login link phone-sized without enlarging the surrounding prose", async () => {
  render(<MemoryRouter><Join /></MemoryRouter>);
  await screen.findByRole("heading", { name: "Join Grace" });
  const link = screen.getByRole("link", { name: "Log in" });
  expect(link).toHaveAttribute("href", "/login");
  expect(link).toHaveClass("inline-flex", "min-h-11", "items-center");
  expect(link.parentElement?.className).not.toMatch(/leading-/);
});
