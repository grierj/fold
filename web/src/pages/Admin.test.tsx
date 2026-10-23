import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { AdminAccountRequests, ApiError } from "../api";
import { ERROR_COPY } from "../lib/errors";
import type { AccountRequest } from "../types";
import { AdminPage } from "./Admin";

vi.mock("../auth", () => ({ useAuth: () => ({ user: { id: 1 }, loading: false, isAdmin: true }) }));
vi.mock("../api", async (actual) => ({
  ...(await actual<typeof import("../api")>()),
  Admin: { pending: vi.fn().mockResolvedValue({ groups: [] }), deletions: vi.fn().mockResolvedValue({ groups: [] }) },
  AdminUsers: { list: vi.fn().mockResolvedValue({ users: [] }) },
  AdminGuardianLinks: { list: vi.fn().mockResolvedValue({ guardianLinks: [], nominations: [] }) },
  AdminAccountRequests: { list: vi.fn(), approve: vi.fn() },
  Site: { get: vi.fn().mockResolvedValue({ timezone: "America/Boise", selfSignup: "request" }) },
}));

const request: AccountRequest = {
  id: 7, name: "Pat Smith", email: "pat@example.com", phone: "", accountType: "adult",
  birthDate: "1980-01-01", message: "", source: "household_conversion", status: "pending",
  hasWarnings: false, warnings: { duplicate: false, honeypot: false, rateLimited: false },
  decisionNote: "", expiresAt: "2026-12-01T00:00:00Z", createdAt: "2026-10-22T12:00:00Z",
};

beforeEach(() => {
  vi.mocked(AdminAccountRequests.list).mockResolvedValue({ requests: [request] });
  vi.mocked(AdminAccountRequests.approve).mockReset();
});

it.each([
  "household_conversion_details", "household_conversion_adult", "household_conversion_inviter", "household_conversion_retry",
] as const)("keeps a pending account card and shows actionable copy for %s", async (code) => {
  vi.mocked(AdminAccountRequests.approve).mockRejectedValue(new ApiError(409, "private server text", { code }));
  render(<MemoryRouter><AdminPage /></MemoryRouter>);
  await userEvent.click(await screen.findByRole("button", { name: "Approve" }));
  expect(await screen.findByRole("alert")).toHaveTextContent(ERROR_COPY[code]);
  expect(screen.getByText(request.name)).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Approve" })).toBeEnabled();
  expect(screen.queryByText("private server text")).not.toBeInTheDocument();
});

it("requires evidence for approval with account consent and sends both in one decision", async () => {
  vi.mocked(AdminAccountRequests.list).mockResolvedValue({ requests: [{ ...request, accountType: "teen" }] });
  vi.mocked(AdminAccountRequests.approve).mockResolvedValue({ request, userId: 8 });
  render(<MemoryRouter><AdminPage /></MemoryRouter>);
  await userEvent.click(await screen.findByRole("checkbox", { name: /Grant account consent with approval/ }));
  await userEvent.click(screen.getByRole("button", { name: "Approve" }));
  expect(AdminAccountRequests.approve).not.toHaveBeenCalled();
  expect(screen.getByLabelText("Consent evidence")).toHaveFocus();
  await userEvent.type(screen.getByLabelText("Consent evidence"), "Parent signed paper form");
  await userEvent.click(screen.getByRole("button", { name: "Approve" }));
  expect(AdminAccountRequests.approve).toHaveBeenCalledWith(7, undefined, undefined, "Parent signed paper form");
});
