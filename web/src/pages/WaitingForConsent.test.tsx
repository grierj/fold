import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import WaitingForConsent from "./WaitingForConsent";

const refresh = vi.hoisted(() => vi.fn(async () => {}));
const logout = vi.hoisted(() => vi.fn(async () => {}));
vi.mock("../auth", () => ({ useAuth: () => ({ refresh, logout }) }));

it("focuses the wait explanation and offers only account status and logout", async () => {
  render(<WaitingForConsent />);
  expect(screen.getByRole("heading", { name: "Waiting for consent" })).toHaveFocus();
  expect(screen.queryByRole("navigation")).not.toBeInTheDocument();
  expect(screen.queryByRole("link")).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Check account status" }));
  expect(refresh).toHaveBeenCalledOnce();
  await userEvent.click(screen.getByRole("button", { name: "Log out" }));
  expect(logout).toHaveBeenCalledOnce();
});
