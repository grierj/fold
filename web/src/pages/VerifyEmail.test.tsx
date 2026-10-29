import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";

const { refresh, verifyEmail } = vi.hoisted(() => ({
  refresh: vi.fn(),
  verifyEmail: vi.fn(),
}));

vi.mock("../auth", () => ({
  useAuth: () => ({ refresh }),
}));
vi.mock("../api", async (original) => {
  const actual = await original<typeof import("../api")>();
  return {
    ...actual,
    Profile: { ...actual.Profile, verifyEmail },
  };
});

import { VerifyEmail } from "./VerifyEmail";

beforeEach(() => {
  refresh.mockReset().mockResolvedValue(undefined);
  verifyEmail.mockReset().mockResolvedValue({ verified: true });
});

it("keeps the expired-link profile action phone-sized within ordinary prose", async () => {
  render(<MemoryRouter initialEntries={["/verify-email"]}><VerifyEmail /></MemoryRouter>);
  const link = await screen.findByRole("link", { name: "profile", exact: true });
  expect(link).toHaveAttribute("href", "/profile");
  expect(link).toHaveClass("inline-flex", "min-h-11", "items-center");
  expect(link.parentElement?.className).not.toMatch(/leading-/);
});

it("verifies the exact token, refreshes auth, and offers the shared profile link", async () => {
  render(<MemoryRouter initialEntries={["/verify-email?token=new-address-token"]}><VerifyEmail /></MemoryRouter>);

  expect(await screen.findByText("Your sign-in email is updated. Your other sessions were signed out.")).toBeVisible();
  expect(verifyEmail).toHaveBeenCalledOnce();
  expect(verifyEmail).toHaveBeenCalledWith("new-address-token");
  expect(refresh).toHaveBeenCalledOnce();
  expect(verifyEmail.mock.invocationCallOrder[0]).toBeLessThan(refresh.mock.invocationCallOrder[0]);

  const profile = screen.getByRole("link", { name: "View your profile" });
  expect(profile).toBeVisible();
  expect(profile).toHaveAttribute("href", "/profile");
  expect(profile).toHaveClass("btn-link");
  expect(screen.queryByRole("button", { name: "View your profile" })).not.toBeInTheDocument();
});
