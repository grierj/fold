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
  vi.mocked(Invitations.accept).mockResolvedValue({ pendingConsent: true });
  render(<MemoryRouter initialEntries={["/invite?token=secret"]}><Invite /></MemoryRouter>);
  expect(await screen.findByText(/Your past and upcoming sign-ups will move/)).toBeInTheDocument();
  expect(screen.getByLabelText("Your name")).toHaveAttribute("readonly");
  await userEvent.type(screen.getByLabelText("Choose a password"), "password1234");
  await userEvent.click(screen.getByRole("button", { name: "Create my account" }));
  expect(await screen.findByRole("heading", { name: "Your account is waiting for consent" })).toBeInTheDocument();
  expect(refresh).not.toHaveBeenCalled();
  expect(screen.queryByLabelText("Choose a password")).not.toBeInTheDocument();
  expect(screen.getByRole("status")).not.toHaveTextContent("password1234");
});

it("signs in an adult conversion after acceptance", async () => {
  vi.mocked(Invitations.get).mockResolvedValue({ site: "Our church", siteName: "Our church", inviterName: "Pat", invitation: { id: 1, name: "Mary", email: "mary@example.com", conversion: true, accountType: "adult", expiresAt: "2026-12-01T00:00:00Z" } });
  vi.mocked(Invitations.accept).mockResolvedValue({ pendingConsent: false });
  render(<MemoryRouter initialEntries={["/invite?token=secret"]}><Invite /></MemoryRouter>);
  await screen.findByLabelText("Your name");
  await userEvent.type(screen.getByLabelText("Choose a password"), "password1234");
  await userEvent.click(screen.getByRole("button", { name: "Create my account" }));
  expect(refresh).toHaveBeenCalledOnce();
});
