import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";

const { refresh, cancelEmailChange } = vi.hoisted(() => ({
  refresh: vi.fn(),
  cancelEmailChange: vi.fn(),
}));

vi.mock("../auth", () => ({
  useAuth: () => ({ refresh }),
}));
vi.mock("../api", async (original) => ({
  ...(await original<typeof import("../api")>()),
  Profile: { cancelEmailChange },
}));

import { ApiError } from "../api";
import { CancelEmailChange } from "./CancelEmailChange";

function renderPage(path = "/cancel-email-change?token=cancel-token") {
  return render(<MemoryRouter initialEntries={[path]}><CancelEmailChange /></MemoryRouter>);
}

beforeEach(() => {
  refresh.mockReset().mockResolvedValue(undefined);
  cancelEmailChange.mockReset().mockResolvedValue({ cancelled: true });
});

it("does not cancel the change when the link is opened", () => {
  renderPage();
  expect(screen.getByRole("heading", { level: 1, name: "Cancel email change" })).toBeInTheDocument();
  expect(screen.getByText(/only cancels a pending email change/i)).toBeInTheDocument();
  expect(cancelEmailChange).not.toHaveBeenCalled();
  expect(refresh).not.toHaveBeenCalled();
});

it("confirms the exact token, refreshes auth, and explains the result", async () => {
  renderPage("/cancel-email-change?token=old-address-token");
  await userEvent.click(screen.getByRole("button", { name: "Cancel email change and sign out all sessions" }));

  expect(cancelEmailChange).toHaveBeenCalledOnce();
  expect(cancelEmailChange).toHaveBeenCalledWith("old-address-token");
  expect(refresh).toHaveBeenCalledOnce();
  expect(await screen.findByText("Pending email change cancelled")).toBeInTheDocument();
  expect(screen.getByText(/all sessions have ended/i)).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Reset your password" })).toHaveAttribute("href", "/forgot-password");
});

it("shows friendly API error copy and never server text", async () => {
  cancelEmailChange.mockRejectedValueOnce(new ApiError(400, "private server detail", { code: "link_expired" }));
  renderPage();
  await userEvent.click(screen.getByRole("button", { name: "Cancel email change and sign out all sessions" }));

  expect(await screen.findByRole("alert")).toHaveTextContent("This link has expired or was already used. Ask for a new one and try again.");
  expect(screen.queryByText("private server detail")).not.toBeInTheDocument();
  expect(refresh).not.toHaveBeenCalled();
});

it("handles a missing token without sending a request", () => {
  renderPage("/cancel-email-change");
  expect(screen.getByText("This cancellation link is incomplete")).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Reset your password" })).toHaveAttribute("href", "/forgot-password");
  expect(screen.queryByRole("button")).not.toBeInTheDocument();
  expect(cancelEmailChange).not.toHaveBeenCalled();
});
