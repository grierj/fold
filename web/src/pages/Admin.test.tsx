import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useParams } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Admin, AdminAccountRequests, AdminGroups, AdminJobs, AdminRooms, AdminUsers, Groups, Invitations, People, RoomRequests, Site, ApiError } from "../api";
import { ERROR_COPY, errorText } from "../lib/errors";
import { ContactHistory } from "../contact-history-api";
import type { AccountRequest, GroupSummary, Room, User } from "../types";
import { AdminPage } from "./Admin";

// One stable object, like the real provider: a fresh one per render would re-run the page's load effects.
const auth = vi.hoisted(() => ({ user: { id: 1 }, loading: false, isAdmin: true }));
vi.mock("../auth", () => ({ useAuth: () => auth }));
vi.mock("../contact-history-api", async (actual) => ({ ...await actual<typeof import("../contact-history-api")>(), ContactHistory: { people: vi.fn(), list: vi.fn(), csv: vi.fn() } }));
vi.mock("../api", async (actual) => ({
  ...(await actual<typeof import("../api")>()),
  People: { search: vi.fn() },
  Invitations: { create: vi.fn() },
  Groups: { list: vi.fn(), members: vi.fn().mockResolvedValue({ members: [] }), memberDeparturePreview: vi.fn(), confirmMemberDeparture: vi.fn() },
  AdminGroups: { list: vi.fn(), addMember: vi.fn() },
  Admin: { pending: vi.fn().mockResolvedValue({ groups: [] }), deletions: vi.fn().mockResolvedValue({ groups: [] }), approve: vi.fn(), reject: vi.fn(), deleteGroup: vi.fn(), churchAddress: vi.fn().mockResolvedValue({ address: "Grace Church" }), setChurchAddress: vi.fn(), guardianCopies: vi.fn().mockResolvedValue({ on: false }), setGuardianCopies: vi.fn(), replyTo: vi.fn().mockResolvedValue({ address: "" }), setReplyTo: vi.fn(), pushEnabled: vi.fn().mockResolvedValue({ on: true }), setPushEnabled: vi.fn() },
  AdminUsers: { list: vi.fn().mockResolvedValue({ users: [] }), clearEmailSuppression: vi.fn() },
  AdminGuardianLinks: { list: vi.fn().mockResolvedValue({ guardianLinks: [], nominations: [] }) },
  AdminAccountRequests: { list: vi.fn(), approve: vi.fn(), setSelfSignup: vi.fn() },
  AdminRooms: { list: vi.fn(), create: vi.fn(), update: vi.fn(), setEnabled: vi.fn(), setApproval: vi.fn(), locations: vi.fn(), matchLocation: vi.fn() },
  RoomRequests: { list: vi.fn(), confirm: vi.fn(), reject: vi.fn() },
  AdminJobs: { list: vi.fn(), retry: vi.fn(), dismiss: vi.fn() },
  Site: { get: vi.fn().mockResolvedValue({ name: "Church", timezone: "America/Denver", selfSignup: "off", roomsEnabled: false, pushEnabled: true }) },
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

const user = (id: number, name: string, extra: Partial<User> = {}): User => ({ id, name, displayName: "", email: `${name.toLowerCase().replace(/ /g, ".")}@example.com`, phone: "", hideContactFromLeaders: true, avatarUrl: "", avatarFocalX: 50, avatarFocalY: 50, role: "member", status: "active", hasPassword: true, accountType: "adult", timezone: "UTC", ...extra });
const group = (id: number, name: string, extra: Partial<GroupSummary> = {}): GroupSummary => ({ id, name, description: "", tags: [], imageUrl: "", coverFocalX: 50, coverFocalY: 50, location: "", leaderName: "", memberCount: 1, nextEventAt: null, status: "active", leaderId: 1, visibility: "public", deletionRequested: false, memberLimit: null, ...extra });

beforeEach(() => {
  vi.mocked(AdminJobs.list).mockReset().mockResolvedValue({ jobs: [], more: false });
  vi.mocked(Site.get).mockReset().mockResolvedValue({ name: "Church", timezone: "America/Denver", selfSignup: "off", roomsEnabled: false, pushEnabled: true });
  vi.mocked(AdminRooms.list).mockReset().mockResolvedValue({ rooms: [], needApproval: false });
  vi.mocked(AdminRooms.setEnabled).mockReset();
  vi.mocked(AdminRooms.setApproval).mockReset();
  vi.mocked(AdminRooms.locations).mockReset().mockResolvedValue({ locations: [] });
  vi.mocked(AdminRooms.matchLocation).mockReset();
  vi.mocked(RoomRequests.list).mockReset().mockResolvedValue({ requests: [] });
  vi.mocked(Admin.pending).mockReset().mockResolvedValue({ groups: [] });
  vi.mocked(Admin.approve).mockReset();
  vi.mocked(Admin.reject).mockReset();
  vi.mocked(Admin.deleteGroup).mockReset();
  vi.mocked(ContactHistory.people).mockReset().mockResolvedValue({ people: [] });
  vi.mocked(AdminUsers.list).mockResolvedValue({ users: [] });
  vi.mocked(AdminGroups.list).mockResolvedValue({ groups: [] });
  vi.mocked(AdminAccountRequests.list).mockResolvedValue({ requests: [request] });
  vi.mocked(AdminAccountRequests.approve).mockReset();
  vi.mocked(Groups.members).mockReset().mockResolvedValue({ members: [] });
  vi.mocked(Groups.list).mockReset().mockResolvedValue({ groups: [{ id: 3, name: "Choir" } as import("../types").GroupSummary] });
  vi.mocked(Invitations.create).mockReset().mockResolvedValue({} as never);
  vi.mocked(AdminAccountRequests.setSelfSignup).mockReset();
  vi.mocked(Groups.memberDeparturePreview).mockReset();
  vi.mocked(Groups.confirmMemberDeparture).mockReset();
});

it("keeps all six sections in one scrolling row with shared chip targets", async () => {
  render(<MemoryRouter initialEntries={["/admin"]}><AdminPage /></MemoryRouter>);
  await screen.findByText(request.name);
  const navigation = screen.getByRole("tablist", { name: "Admin sections" });
  expect(navigation).toHaveClass("flex", "min-w-max");
  expect(navigation).not.toHaveClass("grid", "flex-wrap");
  expect(navigation.parentElement).toHaveClass("overflow-x-auto");
  const buttons = within(navigation).getAllByRole("tab");
  expect(buttons).toHaveLength(6);
  for (const button of buttons) expect(button).toHaveClass("chip");
  expect(buttons[0]).toHaveAccessibleName("Approvals, 1 waiting");
  expect(buttons[0].textContent).toBe("Approvals 1");
  expect(within(buttons[0]).getByText("1")).toHaveAttribute("aria-hidden", "true");
});

it("counts users and groups on their tabs once they are known", async () => {
  vi.mocked(AdminUsers.list).mockResolvedValue({ users: [user(1, "Ann"), user(2, "Bo")] });
  vi.mocked(AdminGroups.list).mockResolvedValue({ groups: [group(1, "A"), group(2, "B"), group(3, "C")] });
  render(<MemoryRouter initialEntries={["/admin"]}><AdminPage /></MemoryRouter>);
  expect(await screen.findByRole("tab", { name: "Users, 2" })).toHaveTextContent("Users 2");
  expect(await screen.findByRole("tab", { name: "Groups, 3" })).toHaveTextContent("Groups 3");
  expect(screen.getByRole("tab", { name: "Invite" })).toHaveTextContent(/^Invite$/);
  expect(AdminGroups.list).toHaveBeenCalledTimes(1);
});

it("shows no count on a tab whose list failed to load", async () => {
  vi.mocked(AdminGroups.list).mockRejectedValue(new ApiError(500, "x"));
  render(<MemoryRouter initialEntries={["/admin"]}><AdminPage /></MemoryRouter>);
  await screen.findByText(request.name);
  expect(screen.getByRole("tab", { name: "Groups" })).toHaveTextContent(/^Groups$/);
});

it("keeps daily work above settings and opens the settings forms only on demand", async () => {
  render(<MemoryRouter initialEntries={["/admin"]}><AdminPage /></MemoryRouter>);
  await screen.findByText(request.name);
  expect(screen.getByRole("tab", { name: /^Approvals/ })).toHaveAttribute("aria-selected", "true");
  expect(screen.queryByRole("combobox", { name: "Church time zone" })).not.toBeInTheDocument();
  expect(screen.queryByRole("link", { name: "Schedule a church-wide event" })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("tab", { name: "Settings" }));
  expect(await screen.findByRole("combobox", { name: "Church time zone" })).toBeInTheDocument();
  expect(screen.getByRole("group", { name: "Self-signup" })).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Schedule a church-wide event" })).toHaveAttribute("href", "/events/new");
  expect(screen.getByRole("tab", { name: "Settings" })).toHaveAttribute("aria-selected", "true");
  await userEvent.click(screen.getByRole("tab", { name: /^Users/ }));
  expect(screen.queryByRole("combobox", { name: "Church time zone" })).not.toBeInTheDocument();
});

it("opens a bookmarked settings tab and falls back to approvals for unknown tabs", async () => {
  const view = render(<MemoryRouter initialEntries={["/admin?tab=settings"]}><AdminPage /></MemoryRouter>);
  expect(await screen.findByRole("heading", { name: "Church settings" })).toBeInTheDocument();
  view.unmount();
  render(<MemoryRouter initialEntries={["/admin?tab=not-a-tab"]}><AdminPage /></MemoryRouter>);
  expect(await screen.findByText(request.name)).toBeInTheDocument();
  expect(screen.getByRole("tab", { name: /^Approvals/ })).toHaveAttribute("aria-selected", "true");
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

it("labels only the groups that need attention and keeps one action beside More", async () => {
  vi.mocked(AdminGroups.list).mockResolvedValue({ groups: [group(3, "Choir", { leaderName: "Pat Leader" })] });
  render(<MemoryRouter initialEntries={["/admin?tab=groups"]}><AdminPage /></MemoryRouter>);
  expect(await screen.findByText("1 member · Organized by Pat Leader")).toBeInTheDocument();
  expect(screen.queryByText("Active", { exact: true })).not.toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Choir" })).toHaveAttribute("href", "/groups/3");
  expect(screen.queryByRole("link", { name: "Open & edit" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Manage members" })).toHaveClass("btn-secondary");
  expect(screen.queryByRole("button", { name: "Delete" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "More for Choir" })).toBeInTheDocument();
});

it("lists pending groups first, then the rest by name, with a label only when not active", async () => {
  vi.mocked(AdminGroups.list).mockResolvedValue({ groups: [
    group(1, "Zeta"), group(2, "Alpha", { status: "archived" }), group(3, "Mid", { status: "pending" }), group(4, "Beta"), group(5, "Aaa pending", { status: "pending" }),
  ] });
  render(<MemoryRouter initialEntries={["/admin?tab=groups"]}><AdminPage /></MemoryRouter>);
  await screen.findByText("Aaa pending");
  const names = screen.getAllByRole("link").map((l) => l.textContent).filter((t) => ["Zeta", "Alpha", "Mid", "Beta", "Aaa pending"].includes(t ?? ""));
  expect(names).toEqual(["Aaa pending", "Mid", "Alpha", "Beta", "Zeta"]);
  expect(screen.getAllByText("Pending approval")).toHaveLength(2);
  expect(screen.getAllByText("Archived")).toHaveLength(1);
  expect(screen.queryByText("Active", { exact: true })).not.toBeInTheDocument();
});

it("filters groups as you type and says when none match", async () => {
  vi.mocked(AdminGroups.list).mockResolvedValue({ groups: [group(1, "Choir", { leaderName: "Pat Leader" }), group(2, "Youth night")] });
  render(<MemoryRouter initialEntries={["/admin?tab=groups"]}><AdminPage /></MemoryRouter>);
  await userEvent.type(await screen.findByRole("searchbox", { name: "Search groups" }), "CHOIR");
  expect(screen.getByRole("link", { name: "Choir" })).toBeInTheDocument();
  expect(screen.queryByRole("link", { name: "Youth night" })).not.toBeInTheDocument();
  await userEvent.clear(screen.getByRole("searchbox", { name: "Search groups" }));
  await userEvent.type(screen.getByRole("searchbox", { name: "Search groups" }), "zzz");
  expect(screen.getByText("No groups match")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Clear search" }));
  expect(screen.getByRole("link", { name: "Youth night" })).toBeInTheDocument();
});

it("deletes a group from its More menu after the same confirm", async () => {
  vi.mocked(AdminGroups.list).mockResolvedValue({ groups: [group(3, "Choir"), group(4, "Youth")] });
  vi.mocked(Admin.deleteGroup).mockResolvedValue({});
  const confirmSpy = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValue(true);
  render(<MemoryRouter initialEntries={["/admin?tab=groups"]}><AdminPage /></MemoryRouter>);
  await userEvent.click(await screen.findByRole("button", { name: "More for Choir" }));
  await userEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
  expect(confirmSpy).toHaveBeenCalledWith("Delete “Choir”? This removes the group, its members, and its events.");
  expect(Admin.deleteGroup).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "More for Choir" }));
  await userEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
  expect(Admin.deleteGroup).toHaveBeenCalledWith(3);
  await waitFor(() => expect(screen.queryByRole("link", { name: "Choir" })).not.toBeInTheDocument());
  const status = screen.getByText("“Choir” was deleted.");
  expect(status).toHaveAttribute("role", "status");
  await waitFor(() => expect(status).toHaveFocus());
  expect(screen.getByRole("tab", { name: "Groups, 1" })).toBeInTheDocument();
  expect(AdminGroups.list).toHaveBeenCalledTimes(1);
});

it("keeps the delete status line in the page, empty, before anything is deleted", async () => {
  vi.mocked(AdminGroups.list).mockResolvedValue({ groups: [group(3, "Choir")] });
  render(<MemoryRouter initialEntries={["/admin?tab=groups"]}><AdminPage /></MemoryRouter>);
  await screen.findByRole("link", { name: "Choir" });
  const statuses = screen.getAllByRole("status").filter((el) => el.textContent === "");
  expect(statuses.length).toBeGreaterThan(0);
  expect(statuses[0]).not.toHaveClass("empty:hidden");
});

it("counts two deletes in a row from the one list", async () => {
  vi.mocked(AdminGroups.list).mockResolvedValue({ groups: [group(3, "Choir"), group(4, "Youth"), group(5, "Band")] });
  vi.mocked(Admin.deleteGroup).mockResolvedValue({});
  vi.spyOn(window, "confirm").mockReturnValue(true);
  render(<MemoryRouter initialEntries={["/admin?tab=groups"]}><AdminPage /></MemoryRouter>);
  await userEvent.click(await screen.findByRole("button", { name: "More for Choir" }));
  await userEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
  await screen.findByRole("tab", { name: "Groups, 2" });
  await userEvent.click(screen.getByRole("button", { name: "More for Youth" }));
  await userEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
  await screen.findByRole("tab", { name: "Groups, 1" });
  expect(AdminGroups.list).toHaveBeenCalledTimes(1);
});

it("loads the groups once for the chip and the tab, and retries a failed load", async () => {
  vi.mocked(AdminGroups.list).mockRejectedValueOnce(new ApiError(500, "x")).mockResolvedValue({ groups: [group(3, "Choir")] });
  render(<MemoryRouter initialEntries={["/admin?tab=groups"]}><AdminPage /></MemoryRouter>);
  await userEvent.click(await screen.findByRole("button", { name: "Try again" }));
  expect(await screen.findByRole("link", { name: "Choir" })).toBeInTheDocument();
  expect(screen.getByRole("tab", { name: "Groups, 1" })).toBeInTheDocument();
  expect(AdminGroups.list).toHaveBeenCalledTimes(2);
});

it("does not refetch the groups when the tab opens, and refetches them once per group decision", async () => {
  const pending = group(9, "Sunrise study", { status: "pending" });
  vi.mocked(Admin.pending).mockResolvedValue({ groups: [pending] });
  vi.mocked(AdminAccountRequests.list).mockResolvedValue({ requests: [] });
  vi.mocked(Admin.approve).mockResolvedValue({ group: pending });
  vi.mocked(AdminGroups.list).mockResolvedValue({ groups: [pending] });
  render(<MemoryRouter initialEntries={["/admin"]}><AdminPage /></MemoryRouter>);
  await screen.findByText("Sunrise study");
  await userEvent.click(screen.getByRole("tab", { name: "Groups, 1" }));
  await screen.findByRole("button", { name: "Manage members" });
  expect(AdminGroups.list).toHaveBeenCalledTimes(1);
  await userEvent.click(screen.getByRole("tab", { name: /^Approvals/ }));
  await userEvent.click(await screen.findByRole("button", { name: "Approve" }));
  await screen.findByText(/approved/);
  expect(AdminGroups.list).toHaveBeenCalledTimes(2);
});

it("leaves room before the Invite only label only when a status label comes first", async () => {
  vi.mocked(AdminGroups.list).mockResolvedValue({ groups: [
    group(1, "Open book", { visibility: "invite_only" }),
    group(2, "Waiting", { visibility: "invite_only", status: "pending" }),
  ] });
  render(<MemoryRouter initialEntries={["/admin?tab=groups"]}><AdminPage /></MemoryRouter>);
  await screen.findByText("Waiting");
  const pills = screen.getAllByText("Invite only");
  expect(pills).toHaveLength(2);
  const [alone, withStatus] = [pills.find((p) => p.closest("div.bg-surface")?.textContent?.includes("Open book"))!, pills.find((p) => p.closest("div.bg-surface")?.textContent?.includes("Waiting"))!];
  expect(alone).not.toHaveClass("ml-1.5");
  expect(withStatus).toHaveClass("ml-1.5");
});

describe("Users search and filters", () => {
  const people = [
    user(1, "Ann Admin", { role: "admin" }),
    user(2, "Bo Member", { displayName: "Bobby" }),
    user(3, "Cy Off", { status: "disabled" }),
    user(4, "Di Invited", { status: "invited" }),
  ];
  const rows = () => screen.getAllByRole("row").slice(1);

  it("filters by name, display name or email as you type, ignoring case", async () => {
    vi.mocked(AdminUsers.list).mockResolvedValue({ users: people });
    render(<MemoryRouter initialEntries={["/admin?tab=users"]}><AdminPage /></MemoryRouter>);
    const box = await screen.findByRole("searchbox", { name: "Search by name or email" });
    expect(rows()).toHaveLength(4);
    await userEvent.type(box, "BOBBY");
    expect(rows()).toHaveLength(1);
    await userEvent.clear(box);
    await userEvent.type(box, "di.invited@");
    expect(rows()).toHaveLength(1);
    expect(rows()[0]).toHaveTextContent("Di Invited");
    await userEvent.clear(box);
    await userEvent.type(box, "ann admin");
    expect(rows()).toHaveLength(1);
  });

  it.each([["Admins", "Ann Admin"], ["Disabled", "Cy Off"], ["Invited", "Di Invited"]])("the %s chip keeps only %s", async (chip, name) => {
    vi.mocked(AdminUsers.list).mockResolvedValue({ users: people });
    render(<MemoryRouter initialEntries={["/admin?tab=users"]}><AdminPage /></MemoryRouter>);
    expect(await screen.findByRole("button", { name: "All" })).toHaveAttribute("aria-pressed", "true");
    await userEvent.click(screen.getByRole("button", { name: chip }));
    expect(screen.getByRole("button", { name: chip })).toHaveAttribute("aria-pressed", "true");
    expect(rows()).toHaveLength(1);
    expect(rows()[0]).toHaveTextContent(name);
    await userEvent.click(screen.getByRole("button", { name: "All" }));
    expect(rows()).toHaveLength(4);
  });

  it("says No one matches in a neutral empty state and can clear both", async () => {
    vi.mocked(AdminUsers.list).mockResolvedValue({ users: people });
    render(<MemoryRouter initialEntries={["/admin?tab=users"]}><AdminPage /></MemoryRouter>);
    await userEvent.click(await screen.findByRole("button", { name: "Admins" }));
    await userEvent.type(screen.getByRole("searchbox", { name: "Search by name or email" }), "nobody");
    expect(screen.getByText("No one matches")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Clear search and filter" }));
    expect(rows()).toHaveLength(4);
    expect(screen.getByRole("button", { name: "All" })).toHaveAttribute("aria-pressed", "true");
  });
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
  expect(screen.getByText(/Adding a teen needs their approval/)).toHaveAttribute("role", "status");
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

it("saves self-signup the moment it changes and says Saved", async () => {
  vi.mocked(AdminAccountRequests.setSelfSignup).mockResolvedValue({ selfSignup: "request" });
  render(<MemoryRouter initialEntries={["/admin?tab=settings"]}><AdminPage /></MemoryRouter>);
  const mode = await screen.findByRole("group", { name: "Self-signup" });
  await waitFor(() => expect(within(mode).getByRole("radio", { name: /^Off/ })).toBeChecked());
  expect(screen.queryByRole("button", { name: "Save self-signup" })).not.toBeInTheDocument();
  await userEvent.click(within(mode).getByRole("radio", { name: /^On/ }));
  expect(AdminAccountRequests.setSelfSignup).toHaveBeenCalledWith("request");
  expect(await screen.findByText("Saved")).toBeInTheDocument();
  expect(within(mode).getByRole("radio", { name: /^On/ })).toBeChecked();
});

it("puts self-signup back and says why when saving fails", async () => {
  vi.mocked(AdminAccountRequests.setSelfSignup).mockRejectedValue(new ApiError(500, "private server text"));
  render(<MemoryRouter initialEntries={["/admin?tab=settings"]}><AdminPage /></MemoryRouter>);
  const mode = await screen.findByRole("group", { name: "Self-signup" });
  await waitFor(() => expect(within(mode).getByRole("radio", { name: /^Off/ })).toBeChecked());
  await userEvent.click(within(mode).getByRole("radio", { name: /^On/ }));
  expect(await screen.findByRole("alert")).toHaveTextContent("We couldn't save that setting.");
  expect(within(mode).getByRole("radio", { name: /^Off/ })).toBeChecked();
  expect(screen.queryByText("Saved")).not.toBeInTheDocument();
  expect(screen.queryByText("private server text")).not.toBeInTheDocument();
});

it("disables self-signup while a save is in flight, then goes back to the value from before that save on failure", async () => {
  let fail: (e: unknown) => void = () => {};
  vi.mocked(AdminAccountRequests.setSelfSignup).mockReturnValue(new Promise((_, reject) => { fail = reject; }));
  render(<MemoryRouter initialEntries={["/admin?tab=settings"]}><AdminPage /></MemoryRouter>);
  const mode = await screen.findByRole("group", { name: "Self-signup" });
  await waitFor(() => expect(within(mode).getByRole("radio", { name: /^Off/ })).toBeChecked());
  await userEvent.click(within(mode).getByRole("radio", { name: /^On/ }));
  expect(within(mode).getByRole("radio", { name: /^On/ })).toBeDisabled();
  expect(within(mode).getByRole("radio", { name: /^Off/ })).toBeDisabled();
  await userEvent.click(within(mode).getByRole("radio", { name: /^Off/ }));
  expect(AdminAccountRequests.setSelfSignup).toHaveBeenCalledOnce();
  await act(async () => { fail(new ApiError(500, "x")); });
  expect(await screen.findByRole("alert")).toHaveTextContent("We couldn't save that setting.");
  expect(within(mode).getByRole("radio", { name: /^Off/ })).toBeChecked();
  expect(within(mode).getByRole("radio", { name: /^Off/ })).toBeEnabled();
});

it("shows Saved in the success tone", async () => {
  vi.mocked(AdminAccountRequests.setSelfSignup).mockResolvedValue({ selfSignup: "request" });
  render(<MemoryRouter initialEntries={["/admin?tab=settings"]}><AdminPage /></MemoryRouter>);
  const mode = await screen.findByRole("group", { name: "Self-signup" });
  await waitFor(() => expect(within(mode).getByRole("radio", { name: /^Off/ })).toBeChecked());
  await userEvent.click(within(mode).getByRole("radio", { name: /^On/ }));
  expect(await screen.findByText("Saved")).toHaveClass("text-success");
});

it("keeps the church time zone behind its Save button", async () => {
  render(<MemoryRouter initialEntries={["/admin?tab=settings"]}><AdminPage /></MemoryRouter>);
  expect(await screen.findByRole("button", { name: "Save time zone" })).toBeDisabled();
});

it("invites without a group by default, confirms in a status and clears the form", async () => {
  render(<MemoryRouter initialEntries={["/admin?tab=invite"]}><AdminPage /></MemoryRouter>);
  const group = await screen.findByRole("combobox", { name: /^Group/ });
  expect(within(group).getByRole("option", { name: "Don't add to a group yet" })).toBeInTheDocument();
  expect(screen.queryByRole("option", { name: /they join Fold only/ })).not.toBeInTheDocument();
  await userEvent.type(screen.getByLabelText("Email"), "new@example.com");
  await userEvent.click(screen.getByRole("button", { name: "Send invitation" }));
  expect(Invitations.create).toHaveBeenCalledWith({ email: "new@example.com", name: undefined, groupId: null });
  expect(await screen.findByRole("status")).toHaveTextContent("Invitation sent to new@example.com.");
  expect(screen.getByLabelText("Email")).toHaveValue("");
});

describe("Approvals: new groups", () => {
  const pending = group(9, "Sunrise study", { status: "pending", tags: ["music", "worship"], leaderName: "Lee Leader" });
  const open = async () => {
    vi.mocked(Admin.pending).mockResolvedValue({ groups: [pending] });
    vi.mocked(AdminAccountRequests.list).mockResolvedValue({ requests: [] });
    render(<MemoryRouter initialEntries={["/admin"]}><AdminPage /></MemoryRouter>);
    await screen.findByText("Sunrise study");
  };

  it("shows the leader's tags as text and opens the tags field only on Edit tags", async () => {
    await open();
    expect(screen.getByText("Tags: music, worship")).toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: /^Tags/ })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Edit tags for Sunrise study" }));
    expect(screen.getByRole("textbox", { name: /^Tags/ })).toHaveValue("music, worship");
    expect(screen.queryByText("Tags: music, worship")).not.toBeInTheDocument();
  });

  it("moves focus to the tags field when it opens, and names each Edit tags button by its group", async () => {
    vi.mocked(Admin.pending).mockResolvedValue({ groups: [pending, group(10, "Evening prayer", { status: "pending" })] });
    vi.mocked(AdminAccountRequests.list).mockResolvedValue({ requests: [] });
    render(<MemoryRouter initialEntries={["/admin"]}><AdminPage /></MemoryRouter>);
    await screen.findByText("Evening prayer");
    const edit = screen.getByRole("button", { name: "Edit tags for Sunrise study" });
    expect(edit).toHaveTextContent(/^Edit tags$/);
    expect(screen.getByRole("button", { name: "Edit tags for Evening prayer" })).toBeInTheDocument();
    await userEvent.click(edit);
    expect(screen.getByRole("textbox", { name: /^Tags/ })).toHaveFocus();
  });

  it("approves without tags unless they were edited, then says so with a link", async () => {
    vi.mocked(Admin.approve).mockResolvedValue({ group: pending });
    await open();
    vi.mocked(Admin.pending).mockResolvedValue({ groups: [] });
    await userEvent.click(screen.getByRole("button", { name: "Approve" }));
    expect(Admin.approve).toHaveBeenCalledWith(9, undefined);
    const status = await screen.findByText(/“Sunrise study” approved\. Lee Leader was told\./);
    expect(status).toHaveAttribute("role", "status");
    expect(within(status).getByRole("link", { name: "View group" })).toHaveAttribute("href", "/groups/9");
    await waitFor(() => expect(status).toHaveFocus());
    expect(screen.getByText("All caught up")).toBeInTheDocument();
  });

  it("sends edited tags with the approval", async () => {
    vi.mocked(Admin.approve).mockResolvedValue({ group: pending });
    await open();
    await userEvent.click(screen.getByRole("button", { name: "Edit tags for Sunrise study" }));
    await userEvent.clear(screen.getByRole("textbox", { name: /^Tags/ }));
    await userEvent.type(screen.getByRole("textbox", { name: /^Tags/ }), "kids, , prayer");
    await userEvent.click(screen.getByRole("button", { name: "Approve" }));
    expect(Admin.approve).toHaveBeenCalledWith(9, ["kids", "prayer"]);
  });

  it("opens a reject dialog instead of the browser confirm, with focus in the message", async () => {
    const confirmSpy = vi.spyOn(window, "confirm");
    await open();
    const reject = screen.getByRole("button", { name: "Reject" });
    await userEvent.click(reject);
    const dialog = screen.getByRole("dialog", { name: "Reject “Sunrise study”?" });
    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(confirmSpy).not.toHaveBeenCalled();
    const message = within(dialog).getByLabelText("Message to the leader (optional)");
    expect(message).toHaveFocus();
    expect(within(dialog).getByText("The group is archived and its leaders are told.")).toBeInTheDocument();
    expect(within(dialog).getByText("They'll see this in Fold. Leave out email addresses and phone numbers.")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Reject group" })).toHaveClass("btn-danger");
    expect(Admin.reject).not.toHaveBeenCalled();
  });

  it("falls back to Its leaders when the group has no leader name", async () => {
    vi.mocked(Admin.approve).mockResolvedValue({ group: pending });
    vi.mocked(Admin.pending).mockResolvedValue({ groups: [group(9, "Sunrise study", { status: "pending", leaderName: "" })] });
    vi.mocked(AdminAccountRequests.list).mockResolvedValue({ requests: [] });
    render(<MemoryRouter initialEntries={["/admin"]}><AdminPage /></MemoryRouter>);
    await screen.findByText("Sunrise study");
    vi.mocked(Admin.pending).mockResolvedValue({ groups: [] });
    await userEvent.click(screen.getByRole("button", { name: "Approve" }));
    expect(await screen.findByText(/“Sunrise study” approved\. Its leaders were told\./)).toBeInTheDocument();
  });

  it("cancels with the Cancel button or Escape and returns focus to Reject", async () => {
    await open();
    const reject = screen.getByRole("button", { name: "Reject" });
    await userEvent.click(reject);
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(reject).toHaveFocus();
    await userEvent.click(reject);
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(reject).toHaveFocus();
    expect(Admin.reject).not.toHaveBeenCalled();
  });

  it("keeps Tab inside the dialog", async () => {
    await open();
    await userEvent.click(screen.getByRole("button", { name: "Reject" }));
    const dialog = screen.getByRole("dialog");
    const message = within(dialog).getByRole("textbox");
    await userEvent.tab({ shift: true });
    expect(within(dialog).getByRole("button", { name: "Cancel" })).toHaveFocus();
    await userEvent.tab();
    expect(message).toHaveFocus();
  });

  it("sends the message, closes, and says the group was rejected without a View group link", async () => {
    vi.mocked(Admin.reject).mockResolvedValue({});
    await open();
    vi.mocked(Admin.pending).mockResolvedValue({ groups: [] });
    await userEvent.click(screen.getByRole("button", { name: "Reject" }));
    await userEvent.type(screen.getByRole("textbox", { name: /^Message to the leader/ }), "Please pick a different name.");
    await userEvent.click(screen.getByRole("button", { name: "Reject group" }));
    expect(Admin.reject).toHaveBeenCalledWith(9, "Please pick a different name.");
    const status = await screen.findByText(/“Sunrise study” rejected\. Lee Leader was told\./);
    expect(status).toHaveAttribute("role", "status");
    expect(within(status).queryByRole("link")).not.toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("rejects with no message when the box is left empty", async () => {
    vi.mocked(Admin.reject).mockResolvedValue({});
    await open();
    await userEvent.click(screen.getByRole("button", { name: "Reject" }));
    await userEvent.click(screen.getByRole("button", { name: "Reject group" }));
    expect(Admin.reject).toHaveBeenCalledWith(9, undefined);
  });

  it.each(["message_has_contact", "too_long"] as const)("shows %s under the field and keeps the dialog open with the text", async (code) => {
    vi.mocked(Admin.reject).mockRejectedValue(new ApiError(400, "private server text", { code }));
    await open();
    await userEvent.click(screen.getByRole("button", { name: "Reject" }));
    const message = screen.getByRole("textbox", { name: /^Message to the leader/ });
    await userEvent.type(message, "Call 555 123 4567");
    await userEvent.click(screen.getByRole("button", { name: "Reject group" }));
    const dialog = screen.getByRole("dialog");
    expect(await within(dialog).findByText(ERROR_COPY[code])).toBeInTheDocument();
    expect(message).toHaveAccessibleDescription(new RegExp(ERROR_COPY[code].slice(0, 20)));
    expect(message).toHaveValue("Call 555 123 4567");
    expect(within(dialog).getByRole("button", { name: "Reject group" })).toBeEnabled();
    expect(screen.queryByText("private server text")).not.toBeInTheDocument();
    expect(screen.getByText("Sunrise study", { selector: "a" })).toBeInTheDocument();
  });

  it("clears the status line when another decision is made", async () => {
    vi.mocked(Admin.approve).mockResolvedValue({ group: pending });
    vi.mocked(AdminAccountRequests.approve).mockResolvedValue({ request, userId: 8 });
    vi.mocked(Admin.pending).mockResolvedValue({ groups: [pending] });
    vi.mocked(AdminAccountRequests.list).mockResolvedValue({ requests: [request] });
    render(<MemoryRouter initialEntries={["/admin"]}><AdminPage /></MemoryRouter>);
    await screen.findByText("Sunrise study");
    const card = screen.getByText("Sunrise study", { selector: "a" }).closest("div.bg-surface") as HTMLElement;
    await userEvent.click(within(card).getByRole("button", { name: "Approve" }));
    await screen.findByText(/“Sunrise study” approved/);
    const accountCard = (await screen.findByText(request.name)).closest("div.bg-surface") as HTMLElement;
    await userEvent.click(within(accountCard).getByRole("button", { name: "Approve" }));
    await waitFor(() => expect(screen.queryByText(/“Sunrise study” approved/)).not.toBeInTheDocument());
  });
});

const hall: Room = { id: 1, name: "Fellowship Hall", building: "Main", capacity: 80, features: "kitchen", notes: "", active: true, restricted: false, defaultSetupMinutes: 30, defaultTeardownMinutes: 0 };
const withRooms = (roomsEnabled: boolean) => vi.mocked(Site.get).mockResolvedValue({ name: "Church", timezone: "America/Denver", selfSignup: "off", roomsEnabled, pushEnabled: true });

it("shows no Rooms tab while rooms are off", async () => {
  render(<MemoryRouter initialEntries={["/admin"]}><AdminPage /></MemoryRouter>);
  await screen.findByText(request.name);
  await waitFor(() => expect(Site.get).toHaveBeenCalled());
  expect(screen.queryByRole("tab", { name: /^Rooms/ })).not.toBeInTheDocument();
  expect(AdminRooms.list).not.toHaveBeenCalled();
});

it("shows the Rooms tab after Groups, with its count, while rooms are on", async () => {
  withRooms(true);
  vi.mocked(AdminRooms.list).mockResolvedValue({ rooms: [hall], needApproval: false });
  render(<MemoryRouter initialEntries={["/admin"]}><AdminPage /></MemoryRouter>);
  const tab = await screen.findByRole("tab", { name: "Rooms, 1" });
  const tabs = screen.getAllByRole("tab").map((t) => t.textContent?.replace(/\d+$/, "").trim());
  expect(tabs.indexOf("Rooms")).toBe(tabs.indexOf("Groups") + 1);
  await userEvent.click(tab);
  expect(await screen.findByText("Fellowship Hall")).toBeInTheDocument();
});

it("falls back to Approvals for a bookmarked Rooms tab when rooms are off", async () => {
  render(<MemoryRouter initialEntries={["/admin?tab=rooms"]}><AdminPage /></MemoryRouter>);
  expect(await screen.findByText(request.name)).toBeInTheDocument();
  expect(screen.getByRole("tab", { name: /^Approvals/ })).toHaveAttribute("aria-selected", "true");
});

it("opens a bookmarked Rooms tab once /site says rooms are on", async () => {
  withRooms(true);
  vi.mocked(AdminRooms.list).mockResolvedValue({ rooms: [hall], needApproval: false });
  render(<MemoryRouter initialEntries={["/admin?tab=rooms"]}><AdminPage /></MemoryRouter>);
  expect(await screen.findByText("Fellowship Hall")).toBeInTheDocument();
  expect(screen.getByRole("tab", { name: /^Rooms/ })).toHaveAttribute("aria-selected", "true");
});

it("asks /site once and hands the answer to the rooms switch", async () => {
  withRooms(true);
  render(<MemoryRouter initialEntries={["/admin?tab=settings"]}><AdminPage /></MemoryRouter>);
  const box = await screen.findByRole("checkbox", { name: /Use rooms/ });
  await waitFor(() => expect(box).toBeChecked());
  // The page, the church time zone, its zone picker and self-signup each read it; the rooms switch no longer does.
  expect(Site.get).toHaveBeenCalledTimes(4);
});

it("says so, with a retry, when /site fails on a bookmarked Rooms tab", async () => {
  vi.mocked(Site.get).mockRejectedValueOnce(new ApiError(500, "private server text"));
  vi.mocked(AdminRooms.list).mockResolvedValue({ rooms: [hall], needApproval: false });
  render(<MemoryRouter initialEntries={["/admin?tab=rooms"]}><AdminPage /></MemoryRouter>);
  expect(await screen.findByRole("alert")).toHaveTextContent("We couldn't load the rooms.");
  expect(screen.queryByText("private server text")).not.toBeInTheDocument();
  withRooms(true);
  await userEvent.click(screen.getByRole("button", { name: "Try again" }));
  expect(await screen.findByText("Fellowship Hall")).toBeInTheDocument();
});

it("reads the rooms switch from /site and saves it the moment it changes, then shows the tab", async () => {
  vi.mocked(AdminRooms.setEnabled).mockResolvedValue({ roomsEnabled: true });
  render(<MemoryRouter initialEntries={["/admin?tab=settings"]}><AdminPage /></MemoryRouter>);
  const box = await screen.findByRole("checkbox", { name: /Use rooms/ });
  expect(box).not.toBeChecked();
  expect(screen.getByText("Book rooms for events and see what's booked. Turning this off hides rooms; nothing is deleted.")).toBeInTheDocument();
  expect(screen.queryByRole("tab", { name: /^Rooms/ })).not.toBeInTheDocument();
  await userEvent.click(box);
  expect(AdminRooms.setEnabled).toHaveBeenCalledWith(true);
  expect(await screen.findByText("Saved")).toHaveClass("text-success");
  expect(box).toBeChecked();
  expect(await screen.findByRole("tab", { name: /^Rooms/ })).toBeInTheDocument();
});

it("starts the rooms switch on when /site says rooms are on, and hides the tab when it is turned off", async () => {
  withRooms(true);
  vi.mocked(AdminRooms.setEnabled).mockResolvedValue({ roomsEnabled: false });
  render(<MemoryRouter initialEntries={["/admin?tab=settings"]}><AdminPage /></MemoryRouter>);
  const box = await screen.findByRole("checkbox", { name: /Use rooms/ });
  await waitFor(() => expect(box).toBeChecked());
  expect(await screen.findByRole("tab", { name: /^Rooms/ })).toBeInTheDocument();
  await userEvent.click(box);
  expect(AdminRooms.setEnabled).toHaveBeenCalledWith(false);
  expect(await screen.findByText("Saved")).toBeInTheDocument();
  expect(box).not.toBeChecked();
  await waitFor(() => expect(screen.queryByRole("tab", { name: /^Rooms/ })).not.toBeInTheDocument());
});

it("disables the rooms switch while it saves and puts it back with an error when saving fails", async () => {
  let fail: (e: unknown) => void = () => {};
  vi.mocked(AdminRooms.setEnabled).mockReturnValue(new Promise((_, reject) => { fail = reject; }));
  render(<MemoryRouter initialEntries={["/admin?tab=settings"]}><AdminPage /></MemoryRouter>);
  const box = await screen.findByRole("checkbox", { name: /Use rooms/ });
  await userEvent.click(box);
  expect(box).toBeDisabled();
  expect(box).toBeChecked();
  await act(async () => { fail(new ApiError(500, "private server text")); });
  expect(await screen.findByRole("alert")).toHaveTextContent("We couldn't save that setting.");
  expect(box).not.toBeChecked();
  expect(box).toBeEnabled();
  expect(screen.queryByText("Saved")).not.toBeInTheDocument();
  expect(screen.queryByText("private server text")).not.toBeInTheDocument();
  expect(screen.queryByRole("tab", { name: /^Rooms/ })).not.toBeInTheDocument();
});

describe("room booking approval (M2.1d)", () => {
  const approval = () => screen.findByRole("checkbox", { name: /Room bookings need an admin's approval/ });
  const waiting = {
    id: 5, eventId: 9, eventTitle: "Weekly Bible study", groupName: "Room 201 class", roomId: 1, roomName: "Fellowship Hall", building: "Main",
    series: true, requestedBy: "Lee Park", dates: 40, busyDates: 0, firstDate: "2026-11-10", firstStartsAt: "2026-11-10T19:00:00Z", createdAt: "2026-10-30T15:00:00Z",
  };

  it("offers the switch only while rooms are on, with its hint", async () => {
    render(<MemoryRouter initialEntries={["/admin?tab=settings"]}><AdminPage /></MemoryRouter>);
    await screen.findByRole("checkbox", { name: /Use rooms/ });
    expect(screen.queryByRole("checkbox", { name: /need an admin's approval/ })).not.toBeInTheDocument();
  });

  it("reads the switch from the rooms list and saves it the moment it changes", async () => {
    withRooms(true);
    vi.mocked(AdminRooms.list).mockResolvedValue({ rooms: [hall], needApproval: false });
    vi.mocked(AdminRooms.setApproval).mockResolvedValue({ needApproval: true });
    render(<MemoryRouter initialEntries={["/admin?tab=settings"]}><AdminPage /></MemoryRouter>);
    const box = await approval();
    await waitFor(() => expect(box).toBeEnabled());
    expect(box).not.toBeChecked();
    expect(screen.getByText("When this is on, leaders' and hosts' bookings wait for an admin. Admins' own bookings are confirmed at once. Turning it off leaves open requests here for you to decide.")).toBeInTheDocument();
    await userEvent.click(box);
    expect(AdminRooms.setApproval).toHaveBeenCalledWith(true);
    expect(box).toBeChecked();
    await waitFor(() => expect(screen.getAllByText("Saved").length).toBeGreaterThan(0));
    expect(box).toBeEnabled();
  });

  it("starts on when approval is on, and puts the switch back with an error when saving fails", async () => {
    withRooms(true);
    vi.mocked(AdminRooms.list).mockResolvedValue({ rooms: [hall], needApproval: true });
    vi.mocked(AdminRooms.setApproval).mockRejectedValue(new ApiError(500, "private server text"));
    render(<MemoryRouter initialEntries={["/admin?tab=settings"]}><AdminPage /></MemoryRouter>);
    const box = await approval();
    await waitFor(() => expect(box).toBeChecked());
    await userEvent.click(box);
    expect(await screen.findByRole("alert")).toHaveTextContent("We couldn't save that setting.");
    expect(box).toBeChecked();
    expect(screen.queryByText("private server text")).not.toBeInTheDocument();
  });

  it("puts the open requests above the rooms and counts them on the Rooms tab", async () => {
    withRooms(true);
    vi.mocked(AdminRooms.list).mockResolvedValue({ rooms: [hall], needApproval: true });
    vi.mocked(RoomRequests.list).mockResolvedValue({ requests: [waiting] });
    render(<MemoryRouter initialEntries={["/admin?tab=rooms"]}><AdminPage /></MemoryRouter>);
    const heading = await screen.findByRole("heading", { name: "Needs a room" });
    expect(screen.getByText("Weekly Bible study")).toBeInTheDocument();
    expect(heading.compareDocumentPosition(await screen.findByText("kitchen", { exact: false }))).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
    expect(screen.getByRole("tab", { name: /^Rooms, 1 waiting/ })).toBeInTheDocument();
    // Like Approvals, the chip shows the waiting count and not the room count too, so its words match its name.
    expect(screen.getByRole("tab", { name: /^Rooms, 1 waiting/ })).toHaveTextContent(/^Rooms 1$/);
  });

  it("asks for typed places only while rooms are on", async () => {
    withRooms(false);
    render(<MemoryRouter initialEntries={["/admin?tab=rooms"]}><AdminPage /></MemoryRouter>);
    await screen.findByText(request.name);
    await waitFor(() => expect(Site.get).toHaveBeenCalled());
    expect(AdminRooms.locations).not.toHaveBeenCalled();
  });

  it("shows Match old locations below the rooms once rooms are on and one is active", async () => {
    withRooms(true);
    vi.mocked(AdminRooms.list).mockResolvedValue({ rooms: [hall], needApproval: false });
    vi.mocked(AdminRooms.locations).mockResolvedValue({ locations: [{ location: "Room 201", dates: 4, events: 2 }] });
    render(<MemoryRouter initialEntries={["/admin?tab=rooms"]}><AdminPage /></MemoryRouter>);
    const heading = await screen.findByRole("heading", { name: "Match old locations" });
    expect(await screen.findByText("4 upcoming dates across 2 events")).toBeInTheDocument();
    expect(screen.getByText("Fellowship Hall", { selector: "span" }).compareDocumentPosition(heading)).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
  });

  it("shows no Needs a room list while approval is off and nothing is waiting", async () => {
    withRooms(true);
    vi.mocked(AdminRooms.list).mockResolvedValue({ rooms: [hall], needApproval: false });
    render(<MemoryRouter initialEntries={["/admin?tab=rooms"]}><AdminPage /></MemoryRouter>);
    await screen.findByText("Fellowship Hall");
    await waitFor(() => expect(RoomRequests.list).toHaveBeenCalled());
    expect(screen.queryByRole("heading", { name: "Needs a room" })).not.toBeInTheDocument();
  });
});

it("shows failed background tasks on the System tab, counted only when there are some", async () => {
  vi.mocked(AdminJobs.list).mockResolvedValue({ jobs: [{ id: 4, kind: "occurrences.extend", label: "Adding upcoming event dates", runsDaily: true, attempts: 5, lastError: "boom", failedAt: "2026-10-30T13:00:00Z" }], more: false });
  render(<MemoryRouter initialEntries={["/admin"]}><AdminPage /></MemoryRouter>);
  const tab = await screen.findByRole("tab", { name: "System, 1" });
  expect(tab).toHaveTextContent("System 1");
  await userEvent.click(tab);
  expect(await screen.findByRole("heading", { name: "Adding upcoming event dates" })).toBeInTheDocument();
  expect(AdminJobs.list).toHaveBeenCalledTimes(1);
});

it("shows no count on the System tab when nothing failed", async () => {
  render(<MemoryRouter initialEntries={["/admin?tab=system"]}><AdminPage /></MemoryRouter>);
  expect(await screen.findByText("Nothing needs attention.")).toBeInTheDocument();
  expect(screen.getByRole("tab", { name: "System" })).toHaveTextContent(/^System$/);
});

it("says more than 100 on the System tab when more tasks failed than the list holds", async () => {
  const many = Array.from({ length: 100 }, (_, i) => ({ id: i + 1, kind: "x", label: `Task ${i}`, runsDaily: false, attempts: 5, lastError: "", failedAt: "2026-10-30T13:00:00Z" }));
  vi.mocked(AdminJobs.list).mockResolvedValue({ jobs: many, more: true });
  render(<MemoryRouter initialEntries={["/admin"]}><AdminPage /></MemoryRouter>);
  expect(await screen.findByRole("tab", { name: "System, more than 100" })).toHaveTextContent("System 100+");
});

describe("email suppressions on the Users tab (M3.2c, DEC-113)", () => {
  const since = "2026-10-20T15:00:00Z";
  const people = () => [
    user(1, "Ann"),
    user(2, "Bo Bounce", { emailSuppression: { reason: "hard_bounce", since } }),
    user(3, "Cy Spam", { emailSuppression: { reason: "complaint", since } }),
    user(4, "Di Manual", { emailSuppression: { reason: "manual", since } }),
  ];

  beforeEach(() => {
    vi.mocked(AdminUsers.clearEmailSuppression).mockReset();
  });

  it("says in words why Fold stopped emailing someone", async () => {
    vi.mocked(AdminUsers.list).mockResolvedValue({ users: people() });
    render(<MemoryRouter initialEntries={["/admin?tab=users"]}><AdminPage /></MemoryRouter>);
    expect(await screen.findByText("Email bouncing")).toBeInTheDocument();
    expect(screen.getByText(/stopped emailing this address on .* because it bounced\. If the address works again, clear this; if you changed their email, there.s nothing to clear\. If your church uses Postmark, reactivate the address there too\./)).toBeInTheDocument();
    expect(screen.getByText("Marked our email as spam")).toBeInTheDocument();
    expect(screen.getByText(/optional email stopped\. Account email, like password resets, still goes\./)).toBeInTheDocument();
    expect(screen.getByText("Email stopped on purpose")).toBeInTheDocument();
    expect(screen.getAllByText(/Email bouncing|Marked our email as spam|Email stopped on purpose/)).toHaveLength(3);
  });

  it("shows the Email problems chip only when someone has one, and filters by it", async () => {
    vi.mocked(AdminUsers.list).mockResolvedValue({ users: [user(1, "Ann")] });
    const view = render(<MemoryRouter initialEntries={["/admin?tab=users"]}><AdminPage /></MemoryRouter>);
    await screen.findByText("Ann");
    expect(screen.queryByRole("button", { name: "Email problems" })).not.toBeInTheDocument();
    view.unmount();

    vi.mocked(AdminUsers.list).mockResolvedValue({ users: people() });
    render(<MemoryRouter initialEntries={["/admin?tab=users"]}><AdminPage /></MemoryRouter>);
    await userEvent.click(await screen.findByRole("button", { name: "Email problems" }));
    expect(screen.queryByText("Ann")).not.toBeInTheDocument();
    expect(screen.getByText("Bo Bounce")).toBeInTheDocument();
    expect(screen.getByText("Cy Spam")).toBeInTheDocument();
  });

  it("clears a block after a confirm, says so, and reloads the list", async () => {
    vi.mocked(AdminUsers.list).mockResolvedValue({ users: people() });
    vi.mocked(AdminUsers.clearEmailSuppression).mockResolvedValue({ cleared: 1 });
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValue(true);
    render(<MemoryRouter initialEntries={["/admin?tab=users"]}><AdminPage /></MemoryRouter>);
    expect(screen.queryByRole("menuitem", { name: "Start emailing again" })).not.toBeInTheDocument();
    await userEvent.click(await screen.findByRole("button", { name: "More for Ann" }));
    expect(screen.queryByRole("menuitem", { name: "Start emailing again" })).not.toBeInTheDocument();
    await userEvent.keyboard("{Escape}");

    await userEvent.click(screen.getByRole("button", { name: "More for Bo Bounce" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Start emailing again" }));
    expect(confirmSpy).toHaveBeenCalledWith("Let Fold email Bo Bounce again?");
    expect(AdminUsers.clearEmailSuppression).not.toHaveBeenCalled();

    const calls = vi.mocked(AdminUsers.list).mock.calls.length;
    vi.mocked(AdminUsers.list).mockResolvedValue({ users: people().map((u) => (u.id === 2 ? { ...u, emailSuppression: undefined } : u)) });
    await userEvent.click(screen.getByRole("button", { name: "More for Bo Bounce" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Start emailing again" }));
    expect(AdminUsers.clearEmailSuppression).toHaveBeenCalledWith(2);
    expect(await screen.findByText("Fold will email Bo Bounce again.")).toHaveAttribute("role", "status");
    await waitFor(() => expect(vi.mocked(AdminUsers.list).mock.calls.length).toBeGreaterThan(calls));
    await waitFor(() => expect(screen.queryByText("Email bouncing")).not.toBeInTheDocument());
  });

  it("keeps the block and says what went wrong when clearing fails", async () => {
    vi.mocked(AdminUsers.list).mockResolvedValue({ users: people() });
    vi.mocked(AdminUsers.clearEmailSuppression).mockRejectedValue(new ApiError(500, "boom"));
    vi.spyOn(window, "confirm").mockReturnValue(true);
    render(<MemoryRouter initialEntries={["/admin?tab=users"]}><AdminPage /></MemoryRouter>);
    await userEvent.click(await screen.findByRole("button", { name: "More for Bo Bounce" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Start emailing again" }));
    expect(await screen.findByText(errorText(new ApiError(500, "boom"), "We couldn't clear that. Please try again."))).toBeInTheDocument();
    expect(screen.getByText("Email bouncing")).toBeInTheDocument();
    expect(screen.queryByText("Fold will email Bo Bounce again.")).not.toBeInTheDocument();
  });
});
