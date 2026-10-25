import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, test, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { ApiError } from "../api";
import { Contacts } from "../contact-api";
import type { ContactGrant } from "../types";
import { GuardianConsentCard } from "./GuardianConsentCard";
import { Guardians } from "../guardian-api";

vi.mock("../guardian-api", () => ({ Guardians: { teens: vi.fn(), claim: vi.fn(), change: vi.fn(), email: vi.fn() } }));

vi.mock("../contact-api", () => ({ Contacts: { grants: vi.fn(), reveal: vi.fn(), revoke: vi.fn() } }));
const teen = { linkId: 1, teenUserId: 2, teenName: "Sam Teen", accountConsent: true, smsConsent: false, photosConsent: false };
const grant: ContactGrant = { id: 1, requestId: 1, requesterId: 10, requesterName: "Lee", subjectUserId: 2, subjectName: "Sam Teen", contextType: "group", fields: "email", needsAdmin: true, grantedAt: "2026-10-02T12:00:00Z", endReason: "", canReveal: false, canRevoke: true };

beforeEach(() => {
  vi.resetAllMocks();
  vi.spyOn(window, "confirm").mockReturnValue(true);
});

test("a failed guardian list can be retried without reloading profile", async () => {
  vi.mocked(Guardians.teens).mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce({ teens: [{ linkId: 1, teenUserId: 2, teenName: "Sam Teen", accountConsent: false, smsConsent: false, photosConsent: false }] });
  render(<GuardianConsentCard />);
  await userEvent.click(await screen.findByRole("button", { name: "Try again" }));
  expect(await screen.findByRole("heading", { name: "Sam Teen" })).toBeVisible();
  expect(screen.getByRole("button", { name: "Grant consent" })).toBeEnabled();
  expect(screen.queryByRole("button", { name: "Try again" })).not.toBeInTheDocument();
});

test("successful account consent revocation closes active grants before the parent reloads; reopening fetches current access", async () => {
  let finishReload!: (value: { teens: typeof teen[] }) => void;
  const reload = new Promise<{ teens: typeof teen[] }>((resolve) => { finishReload = resolve; });
  vi.mocked(Guardians.teens).mockResolvedValueOnce({ teens: [teen] }).mockReturnValueOnce(reload);
  vi.mocked(Guardians.change).mockResolvedValue({});
  vi.mocked(Contacts.grants).mockResolvedValueOnce({ grants: [grant] }).mockResolvedValueOnce({ grants: [] });
  render(<MemoryRouter><GuardianConsentCard /></MemoryRouter>);
  await userEvent.click(await screen.findByRole("button", { name: "View active contact access" }));
  expect(await screen.findByText("Email · Active grant")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Revoke access" })).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Revoke consent" }));
  expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining("They will be signed out"));
  expect(Guardians.change).toHaveBeenCalledWith(1, "account", "revoked");
  expect(screen.queryByRole("region", { name: "Active teen contact grants" })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Revoke access" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "View active contact access" })).toHaveAttribute("aria-expanded", "false");
  await act(async () => { finishReload({ teens: [{ ...teen, accountConsent: false }] }); });
  expect(await screen.findByText("Account consent revoked.")).toBeInTheDocument();
  expect(screen.getByText("Consent not granted")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "View active contact access" }));
  expect(await screen.findByText("No active contact grants")).toBeInTheDocument();
  expect(Contacts.grants).toHaveBeenCalledTimes(2);
  expect(screen.queryByText("Email · Active grant")).not.toBeInTheDocument();
  expect(Contacts.reveal).not.toHaveBeenCalled();
});

test("a grant load pending at consent revocation cannot restore ended access after reopening", async () => {
  let finishOld!: (value: { grants: ContactGrant[] }) => void;
  const old = new Promise<{ grants: ContactGrant[] }>((resolve) => { finishOld = resolve; });
  vi.mocked(Guardians.teens).mockResolvedValueOnce({ teens: [teen] }).mockResolvedValue({ teens: [{ ...teen, accountConsent: false }] });
  vi.mocked(Guardians.change).mockResolvedValue({});
  vi.mocked(Contacts.grants).mockReturnValueOnce(old).mockResolvedValueOnce({ grants: [] });
  render(<MemoryRouter><GuardianConsentCard /></MemoryRouter>);
  await userEvent.click(await screen.findByRole("button", { name: "View active contact access" }));
  expect(await screen.findByText("Loading active grants…")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Revoke consent" }));
  expect(await screen.findByText("Account consent revoked.")).toBeInTheDocument();
  expect(screen.queryByRole("region", { name: "Active teen contact grants" })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "View active contact access" }));
  expect(await screen.findByText("No active contact grants")).toBeInTheDocument();
  await act(async () => { finishOld({ grants: [grant] }); });
  expect(screen.getByText("No active contact grants")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Revoke access" })).not.toBeInTheDocument();
});

test.each(["cancelled", "failed"])("%s consent revocation preserves the expanded current grant", async (outcome) => {
  vi.mocked(Guardians.teens).mockResolvedValue({ teens: [teen] });
  vi.mocked(Contacts.grants).mockResolvedValue({ grants: [grant] });
  if (outcome === "cancelled") vi.mocked(window.confirm).mockReturnValue(false);
  else vi.mocked(Guardians.change).mockRejectedValue(new ApiError(403, "private", { code: "contact_unavailable" }));
  render(<MemoryRouter><GuardianConsentCard /></MemoryRouter>);
  await userEvent.click(await screen.findByRole("button", { name: "View active contact access" }));
  expect(await screen.findByRole("button", { name: "Revoke access" })).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Revoke consent" }));
  if (outcome === "cancelled") expect(Guardians.change).not.toHaveBeenCalled();
  else expect(await screen.findByRole("alert")).toHaveTextContent("no longer available to you");
  expect(screen.getByRole("button", { name: "Close contact access" })).toHaveAttribute("aria-expanded", "true");
  expect(screen.getByRole("button", { name: "Revoke access" })).toBeInTheDocument();
  expect(Guardians.teens).toHaveBeenCalledTimes(1);
});
