import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { Invitations } from "../api";
import { Invite } from "./Invite";

const refresh = vi.hoisted(() => vi.fn());
vi.mock("../auth", () => ({ useAuth: () => ({ refresh }) }));
vi.mock("../api", async (actual) => ({ ...(await actual<typeof import("../api")>()), Invitations: { get: vi.fn(), accept: vi.fn() } }));

beforeEach(() => { vi.resetAllMocks(); });
it("explains conversion and shows teen success without entering signed-in pages", async () => {
  vi.mocked(Invitations.get).mockResolvedValue({ site: "Our church", siteName: "Our church", inviterName: "Pat", invitation: { id: 1, name: "Taylor", email: "taylor@example.com", conversion: true, accountType: "teen", expiresAt: "2026-12-01T00:00:00Z" } });
  vi.mocked(Invitations.accept).mockResolvedValue({ pendingConsent: true, pendingApproval: false });
  render(<MemoryRouter initialEntries={["/invite?token=secret"]}><Invite /></MemoryRouter>);
  expect(await screen.findByText(/Your past and upcoming sign-ups will move/)).toBeInTheDocument();
  expect(screen.getByLabelText("Your name")).toHaveAttribute("readonly");
  await userEvent.type(screen.getByLabelText("Choose a password"), "password1234");
  await userEvent.click(screen.getByRole("button", { name: "Create my account" }));
  const waiting = await screen.findByRole("heading", { name: "Your account is almost ready" });
  expect(waiting).toHaveFocus();
  expect(screen.queryByRole("status")).not.toBeInTheDocument();
  expect(refresh).not.toHaveBeenCalled();
  expect(screen.queryByLabelText("Choose a password")).not.toBeInTheDocument();
  expect(document.body).not.toHaveTextContent("password1234");
});

it("shows an adult the admin-approval wait (Warden, PR 111 F1)", async () => {
  vi.mocked(Invitations.get).mockResolvedValue({ site: "Our church", siteName: "Our church", inviterName: "Pat", invitation: { id: 1, name: "Mary", email: "mary@example.com", conversion: true, accountType: "adult", expiresAt: "2026-12-01T00:00:00Z" } });
  vi.mocked(Invitations.accept).mockResolvedValue({ pendingConsent: false, pendingApproval: true });
  render(<MemoryRouter initialEntries={["/invite?token=secret"]}><Invite /></MemoryRouter>);
  expect(await screen.findByText(/Until then, nothing changes/)).toBeInTheDocument();
  await userEvent.type(screen.getByLabelText("Choose a password"), "password1234");
  await userEvent.click(screen.getByRole("button", { name: "Create my account" }));
  const waiting = await screen.findByRole("heading", { name: "Your account is almost ready" });
  expect(waiting).toHaveFocus();
  expect(screen.getByText(/needs to approve your account/)).toBeInTheDocument();
  expect(refresh).not.toHaveBeenCalled();
  expect(screen.getByText(/your household keeps managing your sign-ups as before/)).toBeInTheDocument();
  expect(screen.getByText(/password you chose/)).toBeInTheDocument();
});
