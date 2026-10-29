import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useParams } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { AdminAccountRequests, AdminGroups, AdminUsers, Groups, People, Site, ApiError } from "../api";
import { ERROR_COPY } from "../lib/errors";
import { ContactHistory } from "../contact-history-api";
import type { AccountRequest, User } from "../types";
import { AdminPage } from "./Admin";

vi.mock("../auth", () => ({ useAuth: () => ({ user: { id: 1 }, loading: false, isAdmin: true }) }));
vi.mock("../contact-history-api", async (actual) => ({ ...await actual<typeof import("../contact-history-api")>(), ContactHistory: { people: vi.fn(), list: vi.fn(), csv: vi.fn() } }));
vi.mock("../api", async (actual) => ({
  ...(await actual<typeof import("../api")>()),
  People: { search: vi.fn() },
  Groups: { members: vi.fn().mockResolvedValue({ members: [] }), memberDeparturePreview: vi.fn(), confirmMemberDeparture: vi.fn() },
  AdminGroups: { list: vi.fn(), addMember: vi.fn() },
  Admin: { pending: vi.fn().mockResolvedValue({ groups: [] }), deletions: vi.fn().mockResolvedValue({ groups: [] }) },
  AdminUsers: { list: vi.fn().mockResolvedValue({ users: [] }) },
  AdminGuardianLinks: { list: vi.fn().mockResolvedValue({ guardianLinks: [], nominations: [] }) },
  AdminAccountRequests: { list: vi.fn(), approve: vi.fn() },
  Site: { get: vi.fn().mockResolvedValue({ name: "Church", timezone: "America/Denver", selfSignup: "off" }) },
}));

function HistoryDestination() {
  const { id } = useParams();
  return <p>Person history {id}</p>;
}

const request: AccountRequest = {
  id: 7, name: "Pat Smith", email: "pat@example.com", phone: "", accountType: "adult",
  birthDate: "1980-01-01", message: "", source: "household_conversion", status: "pending",
  hasWarnings: false, warnings: { duplicate: false, honeypot: false, rateLimited: false },
  decisionNote: "", expiresAt: "2026-12-01T00:00:00Z", createdAt: "2026-10-22T12:00:00Z",
};

beforeEach(() => {
  vi.mocked(Site.get).mockClear();
  vi.mocked(ContactHistory.people).mockReset().mockResolvedValue({ people: [] });
  vi.mocked(AdminUsers.list).mockResolvedValue({ users: [] });
  vi.mocked(AdminGroups.list).mockResolvedValue({ groups: [] });
  vi.mocked(AdminAccountRequests.list).mockResolvedValue({ requests: [request] });
  vi.mocked(AdminAccountRequests.approve).mockReset();
  vi.mocked(Groups.members).mockReset().mockResolvedValue({ members: [] });
  vi.mocked(Groups.memberDeparturePreview).mockReset();
  vi.mocked(Groups.confirmMemberDeparture).mockReset();
});

it("keeps daily work above settings and opens the settings forms only on demand", async () => {
  render(<MemoryRouter initialEntries={["/admin"]}><AdminPage /></MemoryRouter>);
  await screen.findByText(request.name);
  expect(screen.getByRole("button", { name: /^Approvals/ })).toHaveAttribute("aria-pressed", "true");
  expect(screen.queryByRole("combobox", { name: "Church time zone" })).not.toBeInTheDocument();
  expect(screen.queryByRole("link", { name: "Schedule a church-wide event" })).not.toBeInTheDocument();
  expect(Site.get).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "Settings" }));
  expect(await screen.findByRole("combobox", { name: "Church time zone" })).toBeInTheDocument();
  expect(screen.getByRole("group", { name: "Self-signup" })).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Schedule a church-wide event" })).toHaveAttribute("href", "/events/new");
  expect(screen.getByRole("button", { name: "Settings" })).toHaveAttribute("aria-pressed", "true");
  await userEvent.click(screen.getByRole("button", { name: "Users" }));
  expect(screen.queryByRole("combobox", { name: "Church time zone" })).not.toBeInTheDocument();
});

it("opens a bookmarked settings tab and falls back to approvals for unknown tabs", async () => {
  const view = render(<MemoryRouter initialEntries={["/admin?tab=settings"]}><AdminPage /></MemoryRouter>);
  expect(await screen.findByRole("heading", { name: "Church settings" })).toBeInTheDocument();
  view.unmount();
  render(<MemoryRouter initialEntries={["/admin?tab=not-a-tab"]}><AdminPage /></MemoryRouter>);
  expect(await screen.findByText(request.name)).toBeInTheDocument();
  expect(screen.getByRole("button", { name: /^Approvals/ })).toHaveAttribute("aria-pressed", "true");
});

it("shows plain account badges rather than stored status and role codes", async () => {
  const teen: User = { id: 42, name: "Tess Teen", displayName: "Tess", email: "tess@example.com", phone: "", hideContactFromLeaders: true, avatarUrl: "", avatarFocalX: 50, avatarFocalY: 50, role: "member", status: "pending_consent", hasPassword: true, accountType: "teen", timezone: "UTC" };
  vi.mocked(AdminUsers.list).mockResolvedValue({ users: [teen] });
  render(<MemoryRouter initialEntries={["/admin?tab=users"]}><AdminPage /></MemoryRouter>);
  expect(await screen.findByText("Waiting for parent consent")).toBeInTheDocument();
  expect(screen.getByText("Member", { exact: true })).toBeInTheDocument();
  expect(screen.getByText("Teen", { exact: true })).toBeInTheDocument();
  expect(screen.queryByText("pending_consent")).not.toBeInTheDocument();
});

it("labels group metadata and uses the shared size for every group action", async () => {
  vi.mocked(AdminGroups.list).mockResolvedValue({ groups: [{ id: 3, name: "Choir", memberCount: 1, status: "active", leaderName: "Pat Leader" } as import("../types").GroupSummary] });
  render(<MemoryRouter initialEntries={["/admin?tab=groups"]}><AdminPage /></MemoryRouter>);
  expect(await screen.findByText("Active", { exact: true })).toBeInTheDocument();
  expect(screen.getByText("1 member · Organized by Pat Leader")).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Open & edit" })).toHaveClass("btn-secondary");
  expect(screen.getByRole("link", { name: "Open & edit" })).not.toHaveClass("text-sm");
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

it("uses the canonical departure preview and POST for an admin removal", async () => {
  const group = { id: 3, name: "Choir", description: "", memberCount: 1, status: "active", tags: [], imageUrl: "", coverFocalX: 50, coverFocalY: 50, location: "", leaderName: "", nextEventAt: null, leaderId: 1, visibility: "public", deletionRequested: false, memberLimit: null } as import("../types").GroupSummary;
  const member = { id: 9, name: "Pat Person", avatarUrl: "", avatarFocalX: 50, avatarFocalY: 50, role: "member", joinedAt: "2026-10-01T00:00:00Z", admin: false };
  vi.mocked(AdminGroups.list).mockResolvedValue({ groups: [group] });
  vi.mocked(Groups.members).mockResolvedValueOnce({ members: [member] }).mockResolvedValue({ members: [] });
  vi.mocked(Groups.memberDeparturePreview).mockResolvedValue({ departure: { action: "remove", groupId: 3, groupName: "Choir", subjectId: 9, subjectName: "Pat Person", affectedCount: 0, goingCount: 0, maybeCount: 0, waitlistedCount: 0, keepableCount: 0, defaultKeepCount: 0, dates: [], snapshot: "admin-remove" } });
  vi.mocked(Groups.confirmMemberDeparture).mockResolvedValue({ departure: { action: "remove", groupId: 3, subjectId: 9, keptCount: 0, endedCount: 0 } });
  render(<MemoryRouter initialEntries={["/admin?tab=groups"]}><AdminPage /></MemoryRouter>);
  await userEvent.click(await screen.findByRole("button", { name: "Manage members" }));
  await userEvent.click(await screen.findByRole("button", { name: "Remove" }));
  expect(await screen.findByRole("dialog", { name: "Remove Pat Person?" })).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Remove from group" }));
  expect(Groups.confirmMemberDeparture).toHaveBeenCalledWith(3, 9, "admin-remove", [], undefined);
  expect(await screen.findByText("0 dates kept · 0 dates ended.")).toHaveAttribute("role", "status");
});

it.each([401, 403, 404])("clears the protected roster and collapses the manager after departure authority fails with %s", async (status) => {
  const group = { id: 3, name: "Choir", description: "", memberCount: 1, status: "active", tags: [], imageUrl: "", coverFocalX: 50, coverFocalY: 50, location: "", leaderName: "", nextEventAt: null, leaderId: 1, visibility: "public", deletionRequested: false, memberLimit: null } as import("../types").GroupSummary;
  const member = { id: 9, name: "Pat Protected", email: "pat-protected@example.com", phone: "555-0109", avatarUrl: "", avatarFocalX: 50, avatarFocalY: 50, role: "member", joinedAt: "2026-10-01T00:00:00Z", admin: false };
  const denied = new ApiError(status, "private", { code: status === 401 ? "signed_out" : "invalid" });
  vi.mocked(AdminGroups.list).mockResolvedValue({ groups: [group] });
  vi.mocked(Groups.members).mockReset().mockResolvedValueOnce({ members: [member] }).mockRejectedValueOnce(denied);
  vi.mocked(Groups.memberDeparturePreview).mockReset()
    .mockResolvedValueOnce({ departure: { action: "remove", groupId: 3, groupName: "Choir", subjectId: 9, subjectName: "Pat Protected", affectedCount: 0, goingCount: 0, maybeCount: 0, waitlistedCount: 0, keepableCount: 0, defaultKeepCount: 0, dates: [], snapshot: "admin-denied" } })
    .mockRejectedValueOnce(denied);
  vi.mocked(Groups.confirmMemberDeparture).mockReset().mockRejectedValue(
    status === 401 ? new ApiError(409, "private", { code: "departure_unavailable" }) : denied
  );

  render(<MemoryRouter initialEntries={["/admin?tab=groups"]}><AdminPage /></MemoryRouter>);
  await userEvent.click(await screen.findByRole("button", { name: "Manage members" }));
  expect(await screen.findByText("Pat Protected")).toBeInTheDocument();
  expect(screen.getByText(/pat-protected@example\.com/)).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Remove" }));
  await userEvent.click(await screen.findByRole("button", { name: "Remove from group" }));

  expect(await screen.findByRole("heading", { name: "Departure unavailable" })).toBeInTheDocument();
  expect(screen.queryByText("Pat Protected")).not.toBeInTheDocument();
  expect(screen.queryByText(/pat-protected@example\.com/)).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Manage members" })).toHaveFocus();
  expect(screen.queryByText("Pat Protected")).not.toBeInTheDocument();
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

it("opens person history with the exact ID returned for the current email", async () => {
  const person: User = { id: Number("9007199254740993"), name: "Pat History", displayName: "Pat", email: "pat-history@example.com", phone: "", hideContactFromLeaders: true, avatarUrl: "", avatarFocalX: 50, avatarFocalY: 50, role: "member", status: "active", hasPassword: true, accountType: "adult", timezone: "UTC" };
  vi.mocked(AdminUsers.list).mockResolvedValue({ users: [person] });
  vi.mocked(ContactHistory.people).mockResolvedValue({ people: [{ id: "9007199254740993", name: "Pat", email: person.email }] });
  render(<MemoryRouter initialEntries={["/admin?tab=users"]}><Routes>
    <Route path="/admin" element={<AdminPage />} />
    <Route path="/admin/users/:id/contact-history" element={<HistoryDestination />} />
  </Routes></MemoryRouter>);
  expect(await screen.findByRole("link", { name: "Find contact access history" })).toHaveAttribute("href", "/admin/contact-history");
  await userEvent.click(await screen.findByRole("button", { name: "More for Pat" }));
  await userEvent.click(screen.getByRole("menuitem", { name: "Contact access history" }));
  expect(await screen.findByText("Person history 9007199254740993")).toBeInTheDocument();
  expect(ContactHistory.people).toHaveBeenCalledWith({ email: "pat-history@example.com" });
});

it("copies and always shows the exact returned person ID as a fallback", async () => {
  const person: User = { id: Number("9007199254740993"), name: "Pat History", displayName: "Pat", email: "pat-history@example.com", phone: "", hideContactFromLeaders: true, avatarUrl: "", avatarFocalX: 50, avatarFocalY: 50, role: "member", status: "active", hasPassword: true, accountType: "adult", timezone: "UTC" };
  const writeText = vi.fn().mockRejectedValue(new DOMException("denied"));
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
  vi.mocked(AdminUsers.list).mockResolvedValue({ users: [person] });
  vi.mocked(ContactHistory.people).mockResolvedValue({ people: [{ id: "9007199254740993", name: "Pat Public", email: person.email }] });
  render(<MemoryRouter initialEntries={["/admin?tab=users"]}><AdminPage /></MemoryRouter>);
  await userEvent.click(await screen.findByRole("button", { name: "More for Pat" }));
  await userEvent.click(screen.getByRole("menuitem", { name: "Copy person ID" }));
  const status = await screen.findByRole("status");
  expect(status).toHaveTextContent("Person ID for Pat Public: 9007199254740993");
  expect(status).not.toHaveTextContent("pat-history@example.com");
  expect(writeText).toHaveBeenCalledWith("9007199254740993");
});

it("does not fall back to the first user when the selected row has no email", async () => {
  const other: User = { id: 41, name: "First User", displayName: "First", email: "first@example.com", phone: "", hideContactFromLeaders: true, avatarUrl: "", avatarFocalX: 50, avatarFocalY: 50, role: "member", status: "active", hasPassword: true, accountType: "adult", timezone: "UTC" };
  const person: User = { id: 42, name: "No Email", displayName: "No Email", email: "", phone: "", hideContactFromLeaders: true, avatarUrl: "", avatarFocalX: 50, avatarFocalY: 50, role: "member", status: "active", hasPassword: true, accountType: "adult", timezone: "UTC" };
  vi.mocked(AdminUsers.list).mockResolvedValue({ users: [other, person] });
  render(<MemoryRouter initialEntries={["/admin?tab=users"]}><AdminPage /></MemoryRouter>);
  await userEvent.click(await screen.findByRole("button", { name: "More for No Email" }));
  await userEvent.click(screen.getByRole("menuitem", { name: "Contact access history" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Refresh the user list");
  expect(ContactHistory.people).not.toHaveBeenCalled();
});
