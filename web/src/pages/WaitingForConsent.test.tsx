import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import WaitingForConsent from "./WaitingForConsent";

const { auth } = vi.hoisted(() => ({ auth: vi.fn() }));
vi.mock("../auth", () => ({ useAuth: auth }));
beforeEach(() => { auth.mockReturnValue({ user: null }); });

it("explains consent without locking public browsing behind a session", () => {
  render(<MemoryRouter><WaitingForConsent /></MemoryRouter>);
  expect(screen.getByRole("heading", { name: "Your account is almost ready" })).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Browse public events" })).toHaveAttribute("href", "/calendar");
  expect(screen.getByRole("link", { name: "Back to log in" })).toHaveAttribute("href", "/login");
  expect(screen.queryByRole("button")).not.toBeInTheDocument();
  expect(screen.queryByRole("main")).not.toBeInTheDocument(); // the shared shell supplies the landmark and church branding
  expect(screen.getByText(/without logging in/)).toBeInTheDocument();
});

it("does not tell an already signed-in person that consent blocks their login", () => {
  auth.mockReturnValue({ user: { id: 1, status: "active" } });
  render(<MemoryRouter initialEntries={["/waiting-for-consent"]}><Routes>
    <Route path="/waiting-for-consent" element={<WaitingForConsent />} />
    <Route path="/" element={<h1>Groups destination</h1>} />
  </Routes></MemoryRouter>);
  expect(screen.getByRole("heading", { name: "Groups destination" })).toBeInTheDocument();
});
