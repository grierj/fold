import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { Auth } from "../api/auth";
import { ApiError } from "../api/core";
import { ResetPassword } from "./ResetPassword";

const signedIn = vi.fn();
vi.mock("../auth", () => ({ useAuth: () => ({ signedIn }) }));
vi.mock("../api/auth", () => ({ Auth: { confirmPasswordReset: vi.fn() } }));

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/reset-password" element={<ResetPassword />} />
        <Route path="/" element={<p>Home page</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

async function fillAndSubmit(password: string, confirm = password) {
  await userEvent.type(screen.getByLabelText("New password"), password);
  await userEvent.type(screen.getByLabelText("Confirm new password"), confirm);
  await userEvent.click(screen.getByRole("button", { name: "Set new password" }));
}

beforeEach(() => {
  vi.resetAllMocks();
  signedIn.mockResolvedValue("/");
});

it("offers a new reset link when the link has no token", () => {
  renderAt("/reset-password");
  expect(screen.getByText("This link is missing its token.")).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Request a new reset link" })).toHaveAttribute("href", "/forgot-password");
  expect(screen.queryByLabelText("New password")).not.toBeInTheDocument();
});

it("sends the token with the new password, signs the person in and goes where signedIn says", async () => {
  vi.mocked(Auth.confirmPasswordReset).mockResolvedValue({ user: { id: 1 } } as never);
  renderAt("/reset-password?token=abc123");
  await fillAndSubmit("a long enough password");
  expect(await screen.findByText("Home page")).toBeInTheDocument();
  expect(Auth.confirmPasswordReset).toHaveBeenCalledWith("abc123", "a long enough password");
  expect(signedIn).toHaveBeenCalledOnce();
});

it("does not send a password that is too short or does not match its confirmation", async () => {
  renderAt("/reset-password?token=abc123");
  await fillAndSubmit("short");
  expect(await screen.findByText("Password must be at least 10 characters.")).toBeInTheDocument();
  await userEvent.clear(screen.getByLabelText("New password"));
  await userEvent.clear(screen.getByLabelText("Confirm new password"));
  await fillAndSubmit("a long enough password", "a different password");
  expect(await screen.findByText("The passwords don't match.")).toBeInTheDocument();
  expect(Auth.confirmPasswordReset).not.toHaveBeenCalled();
});

it("says an expired or used link cannot be used, and keeps the person on the page", async () => {
  vi.mocked(Auth.confirmPasswordReset).mockRejectedValue(new ApiError(410, "gone", { code: "link_expired" }));
  renderAt("/reset-password?token=old");
  await fillAndSubmit("a long enough password");
  expect(await screen.findByText("This link has expired or was already used. Request a new one below.")).toBeInTheDocument();
  expect(signedIn).not.toHaveBeenCalled();
});
