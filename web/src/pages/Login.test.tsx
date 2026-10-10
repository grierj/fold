import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api";
import { Login } from "./Login";

const login = vi.fn();
vi.mock("../auth", () => ({ useAuth: () => ({ login, user: null }) }));

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

  it("signs in with email and password, then goes home", async () => {
    login.mockResolvedValue(undefined);
    renderLogin();
    await userEvent.type(screen.getByLabelText("Email"), "pat@example.com");
    await userEvent.type(screen.getByLabelText("Password"), "password1234");
    await userEvent.click(screen.getByRole("button", { name: /log in/i }));
    expect(login).toHaveBeenCalledWith("pat@example.com", "password1234");
    expect(await screen.findByText("Groups page")).toBeInTheDocument();
  });

  it("shows one message for any bad email or password", async () => {
    login.mockImplementation(async () => {
      throw new ApiError(401, "invalid credentials");
    });
    renderLogin();
    await userEvent.type(screen.getByLabelText("Email"), "nobody@example.com");
    await userEvent.type(screen.getByLabelText("Password"), "wrong-password");
    await userEvent.click(screen.getByRole("button", { name: /log in/i }));
    expect(await screen.findByText("Invalid email or password.")).toBeInTheDocument();
  });
});
