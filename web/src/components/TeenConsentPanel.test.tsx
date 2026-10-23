import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { AdminTeenConsents, ApiError } from "../api";
import type { TeenConsent, User } from "../types";
import TeenConsentPanel from "./TeenConsentPanel";

vi.mock("../api", async (actual) => ({
  ...(await actual<typeof import("../api")>()),
  AdminTeenConsents: { list: vi.fn(), change: vi.fn() },
}));

const teen: User = {
  id: 7, name: "Casey Teen", displayName: "Casey", email: "casey@example.com", phone: "",
  avatarUrl: "", avatarFocalX: 50, avatarFocalY: 50, role: "member", status: "pending_consent",
  hasPassword: true, accountType: "teen", birthDate: "2011-01-01", timezone: "",
};
const consent: TeenConsent = {
  id: 11, teenUserId: teen.id, scope: "account", status: "granted", grantedBy: 1, granterRole: "admin",
  evidenceText: "Signed parent form", at: "2026-10-22T12:00:00Z",
};

beforeEach(() => {
  vi.mocked(AdminTeenConsents.list).mockReset().mockResolvedValue({ consents: [], administrativelyDisabled: false });
  vi.mocked(AdminTeenConsents.change).mockReset().mockResolvedValue({});
});

it("requires and focuses evidence before an admin grant", async () => {
  render(<TeenConsentPanel user={teen} onUpdated={vi.fn()} onClose={vi.fn()} />);
  await userEvent.click(await screen.findByRole("button", { name: "Grant consent" }));
  expect(screen.getByLabelText("Consent evidence")).toHaveFocus();
  expect(screen.getByText("Write how you received consent first.")).toBeInTheDocument();
  expect(AdminTeenConsents.change).not.toHaveBeenCalled();
});

it("saves the chosen scope and reloads the recorded history", async () => {
  const updated = vi.fn();
  render(<TeenConsentPanel user={teen} onUpdated={updated} onClose={vi.fn()} />);
  await userEvent.click(await screen.findByRole("radio", { name: "SMS" }));
  await userEvent.type(screen.getByLabelText("Consent evidence"), "Parent signed SMS form");
  vi.mocked(AdminTeenConsents.list).mockResolvedValue({ consents: [{ ...consent, scope: "sms", evidenceText: "Parent signed SMS form" }], administrativelyDisabled: false });
  await userEvent.click(screen.getByRole("button", { name: "Grant consent" }));
  expect(AdminTeenConsents.change).toHaveBeenCalledWith(7, { scope: "sms", status: "granted", evidenceText: "Parent signed SMS form" });
  expect(await screen.findByRole("status")).toHaveTextContent("SMS consent granted.");
  expect(updated).toHaveBeenCalledOnce();
  await userEvent.click(screen.getByText("Consent history (1)"));
  expect(screen.getByText("Parent signed SMS form")).toBeVisible();
});

it("confirms account revocation and preserves evidence after an API error", async () => {
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  const updated = vi.fn();
  render(<TeenConsentPanel user={teen} onUpdated={updated} onClose={vi.fn()} />);
  await userEvent.click(await screen.findByRole("button", { name: "Revoke consent" }));
  expect(confirm).toHaveBeenCalledWith(expect.stringContaining("They will be signed out"));
  expect(AdminTeenConsents.change).not.toHaveBeenCalled();
  confirm.mockReturnValue(true);
  vi.mocked(AdminTeenConsents.change).mockRejectedValue(new ApiError(409, "private detail", { code: "consent_teen_only" }));
  await userEvent.type(screen.getByLabelText("Consent evidence"), "Parent withdrew consent");
  await userEvent.click(screen.getByRole("button", { name: "Revoke consent" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("This account is no longer a teen account. Refresh the user list.");
  expect(screen.getByLabelText("Consent evidence")).toHaveValue("Parent withdrew consent");
  expect(updated).not.toHaveBeenCalled();
  confirm.mockRestore();
});

it("explains independent suspension and keeps adult history read-only", async () => {
  vi.mocked(AdminTeenConsents.list).mockResolvedValue({ consents: [consent], administrativelyDisabled: true });
  const view = render(<TeenConsentPanel user={teen} onUpdated={vi.fn()} onClose={vi.fn()} />);
  expect(await screen.findByText(/Granting consent keeps it disabled/)).toBeInTheDocument();
  view.rerender(<TeenConsentPanel user={{ ...teen, accountType: "adult" }} onUpdated={vi.fn()} onClose={vi.fn()} />);
  await waitFor(() => expect(screen.queryByRole("button", { name: "Grant consent" })).not.toBeInTheDocument());
  expect(screen.getByText(/Their teen consent history stays on file/)).toBeVisible();
  await userEvent.click(screen.getByText("Consent history (1)"));
  expect(screen.getByText("Signed parent form")).toBeVisible();
});
