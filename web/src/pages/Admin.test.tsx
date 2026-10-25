import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { AdminAccountRequests, AdminGroups, Groups, People, ApiError } from "../api";
import { ERROR_COPY } from "../lib/errors";
import type { AccountRequest } from "../types";
import { AdminPage } from "./Admin";

vi.mock("../auth", () => ({ useAuth: () => ({ user: { id: 1 }, loading: false, isAdmin: true }) }));
vi.mock("../api", async (actual) => ({
  ...(await actual<typeof import("../api")>()),
  People: { search: vi.fn() },
  Groups: { members: vi.fn().mockResolvedValue({ members: [] }) },
  AdminGroups: { list: vi.fn(), addMember: vi.fn() },
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

it("uses name selection on admin add with the existing role and over-limit choice", async () => {
  vi.mocked(AdminGroups.list).mockResolvedValue({ groups: [{ id: 3, name: "Choir", memberCount: 0, status: "active" } as import("../types").GroupSummary] });
  vi.mocked(People.search).mockResolvedValue({ state: "results", people: [{ selectionId: "opaque", displayName: "Pat", teen: false, sharedGroups: [], otherGroups: [] }] });
  vi.mocked(AdminGroups.addMember).mockRejectedValueOnce(new ApiError(409, "full", { code: "group_full" })).mockResolvedValueOnce({});
  vi.spyOn(window, "confirm").mockReturnValue(true);
  render(<MemoryRouter initialEntries={["/admin?tab=groups"]}><AdminPage /></MemoryRouter>);
  await userEvent.click(await screen.findByRole("button", { name: "Manage members" }));
  await userEvent.type(screen.getByRole("combobox", { name: "Find a person by name" }), "Pat"); await userEvent.click(await screen.findByRole("option", { name: "Pat" }));
  await userEvent.click(screen.getByRole("radio", { name: "Leader" }));
  await userEvent.click(screen.getByRole("button", { name: "Add" }));
  expect(AdminGroups.addMember).toHaveBeenNthCalledWith(1, 3, { selectionId: "opaque" }, "leader");
  expect(AdminGroups.addMember).toHaveBeenLastCalledWith(3, { selectionId: "opaque" }, "leader", true);
  expect(Groups.members).toHaveBeenCalledWith(3);
});

it("offers a teen Ask to add without a leader-role shortcut", async () => {
  vi.mocked(AdminGroups.list).mockResolvedValue({ groups: [{ id: 3, name: "Choir", memberCount: 0, status: "active" } as import("../types").GroupSummary] });
  vi.mocked(AdminGroups.addMember).mockReset();
  vi.mocked(People.search).mockResolvedValue({ state: "results", people: [{ selectionId: "teen", displayName: "Teen Pat", teen: true, sharedGroups: [], otherGroups: [] }] });
  render(<MemoryRouter initialEntries={["/admin?tab=groups"]}><AdminPage /></MemoryRouter>);
  await userEvent.click(await screen.findByRole("button", { name: "Manage members" }));
  await userEvent.type(screen.getByRole("combobox", { name: "Find a person by name" }), "Pat"); await userEvent.click(await screen.findByRole("option", { name: /Teen Pat/ }));
  expect(screen.getByRole("status")).toHaveTextContent("Adding a teen needs their approval");
  expect(screen.getByRole("button", { name: "Ask to add" })).toBeEnabled();
  await userEvent.click(screen.getByRole("button", { name: "Ask to add" }));
  expect(screen.getByRole("button", { name: "Send ask to add" })).toBeInTheDocument();
  expect(AdminGroups.addMember).not.toHaveBeenCalled();
});
