import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api";
import { Login } from "./Login";

const login = vi.fn();
vi.mock("../auth", () => ({ useAuth: () => ({ login, user: null }) }));
vi.mock("../api", async (original) => ({
  ...(await original<typeof import("../api")>()),
  Site: { get: vi.fn().mockResolvedValue({ name: "Grace Community Church" }) },
}));

function renderLogin() {
  return render(
    <MemoryRouter initialEntries={["/login"]}>
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route path="/" element={<p>Groups page</p>} />
      </Routes>
    </MemoryRouter>
  );
}

describe("Login", () => {
  beforeEach(() => {
    login.mockReset();
  });

  it("gives the account-request link a phone-sized target without changing prose line height", () => {
    renderLogin();
    const link = screen.getByRole("link", { name: "Request an account" });
    expect(link).toHaveClass("text-link");
    expect(link).toHaveAttribute("href", "/join");
    expect(link.parentElement?.className).not.toMatch(/leading-/);
  });

  it("names the church in the page title", async () => {
    renderLogin();
    expect(await screen.findByRole("heading", { name: "Log in to Grace Community Church" })).toBeInTheDocument();
  });

  it("signs in with email and password, then goes home", async () => {
    login.mockResolvedValue(undefined);
    renderLogin();
    await userEvent.type(screen.getByLabelText("Email"), "pat@example.com");
    await userEvent.type(screen.getByLabelText("Password"), "password1234");
    await userEvent.click(screen.getByRole("button", { name: /log in/i }));
    expect(login).toHaveBeenCalledWith("pat@example.com", "password1234");
    expect(await screen.findByText("Groups page")).toBeInTheDocument();
  });

  it("explains pending consent without trapping public browsing", async () => {
    login.mockRejectedValue(new ApiError(403, "private", { code: "consent_required" }));
    renderLogin();
    await userEvent.type(screen.getByLabelText("Email"), "teen@example.com");
    await userEvent.type(screen.getByLabelText("Password"), "password1234");
    await userEvent.click(screen.getByRole("button", { name: "Log in" }));
    expect(await screen.findByRole("status")).toHaveTextContent("waiting for a parent or guardian's consent");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Browse public events" })).toHaveAttribute("href", "/calendar");
    expect(screen.getByRole("heading", { name: "Log in to Grace Community Church" })).toBeInTheDocument();
  });

  it("shows one message for any bad email or password", async () => {
    login.mockImplementation(async () => {
      throw new ApiError(401, "invalid credentials");
    });
    renderLogin();
    await userEvent.type(screen.getByLabelText("Email"), "nobody@example.com");
    await userEvent.type(screen.getByLabelText("Password"), "wrong-password");
    await userEvent.click(screen.getByRole("button", { name: /log in/i }));
    expect(await screen.findByText("That email and password don't match. Check them and try again.")).toBeInTheDocument();
  });
});
