import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useNavigate } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { ApiError, Events, Groups, Households, People } from "../api";
import type { GroupDetailResponse, Household, User } from "../types";
import { occurrence } from "../test/occurrence";
import { GroupDetail } from "./GroupDetail";

const actor = vi.hoisted(() => ({ user: { id: 2, accountType: "adult", hideContactFromLeaders: false } as User | null }));
vi.mock("../auth", () => ({ useAuth: () => ({ user: actor.user, loading: false, isAdmin: false }) }));
vi.mock("../api", async (actual) => ({ ...(await actual<typeof import("../api")>()), People: { search: vi.fn() }, Groups: { get: vi.fn(), addMember: vi.fn(), join: vi.fn(), departurePreview: vi.fn(), confirmDeparture: vi.fn(), memberDeparturePreview: vi.fn(), confirmMemberDeparture: vi.fn() }, Events: { register: vi.fn(), registerSeries: vi.fn(), undoSeriesRegistration: vi.fn() }, Households: { mine: vi.fn() } }));
const family: Household = { id: 1, name: "Family", address: "", createdBy: 2, accounts: [], people: [{ id: 3, householdId: 1, canConvert: false, firstName: "Mary", lastName: "", name: "Mary", kind: "adult", photoConsent: false, linkStatus: "none", registrationEligibility: "eligible", createdBy: 2 }] };
const page: GroupDetailResponse = { group: { id: 1, name: "Family group", description: "", tags: [], imageUrl: "", coverFocalX: 50, coverFocalY: 50, location: "Hall", leaderName: "Leader", memberCount: 1, nextEventAt: null, status: "active", leaderId: 1, visibility: "public", deletionRequested: false, memberLimit: null }, members: [], upcomingEvents: [occurrence({})], truncated: false, hasMore: false, nextCursor: "", canManage: false, isMember: true };
function open() { render(<MemoryRouter initialEntries={["/groups/1"]}><Routes><Route path="/groups/:id" element={<GroupDetail />} /></Routes></MemoryRouter>); }
function GroupRouteWithSwitcher() {
  const navigate = useNavigate();
  return <><button onClick={() => navigate("/groups/2")}>Open next group</button><GroupDetail /></>;
}
function openWithSwitcher() {
  render(<MemoryRouter initialEntries={["/groups/1"]}><Routes>
    <Route path="/groups/:id" element={<GroupRouteWithSwitcher />} />
    <Route path="/my-groups" element={<h1>My groups destination</h1>} />
  </Routes></MemoryRouter>);
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((accept, decline) => { resolve = accept; reject = decline; });
  return { promise, resolve, reject };
}
beforeEach(() => { vi.resetAllMocks(); actor.user = { id: 2, accountType: "adult", hideContactFromLeaders: false } as User; vi.mocked(Groups.get).mockResolvedValue(page); vi.mocked(Households.mine).mockResolvedValue({ household: family }); vi.mocked(Groups.departurePreview).mockResolvedValue({ departure: { action: "leave", groupId: 1, groupName: "Family group", subjectId: 2, subjectName: "Member", affectedCount: 0, goingCount: 0, maybeCount: 0, waitlistedCount: 0, keepableCount: 0, defaultKeepCount: 0, dates: [], snapshot: "self" } }); vi.mocked(Groups.confirmDeparture).mockResolvedValue({ departure: { action: "leave", groupId: 1, subjectId: 2, keptCount: 0, endedCount: 0 } }); });

it("fetches the household once and refetches all date state after a batch", async () => {
  vi.mocked(Events.register).mockResolvedValue({ registration: null, people: [{ registrationId: 9, personId: 3, registeredBy: 2, status: "going", attendanceMode: "in_person", waitlistPosition: null, childcareRequested: false }] });
  open(); await userEvent.click(await screen.findByRole("checkbox", { name: "Mary" }));
  await userEvent.click(screen.getByRole("button", { name: "Save answers" }));
  await waitFor(() => expect(Groups.get).toHaveBeenCalledTimes(2));
  expect(Households.mine).toHaveBeenCalledTimes(1);
  expect(Events.register).toHaveBeenCalledWith(7, { date: "2026-12-01", people: [{ personId: 3, status: "going", attendanceMode: "in_person", childcareRequested: false }] });
});

it("serializes household RSVP saves across sibling events and releases without resetting drafts", async () => {
  const events = [occurrence({ eventId: 7 }), occurrence({ id: 2, eventId: 8, date: "2026-12-08" })];
  vi.mocked(Groups.get).mockResolvedValue({ ...page, upcomingEvents: events });
  const pending = deferred<Awaited<ReturnType<typeof Events.register>>>();
  vi.mocked(Events.register).mockReturnValue(pending.promise);

  open();
  const forms = await screen.findAllByRole("form", { name: "Household answers" });
  await userEvent.click(within(forms[0]).getByRole("checkbox", { name: "Mary" }));
  await userEvent.selectOptions(within(forms[0]).getByLabelText("Answer for Mary"), "maybe");
  await userEvent.click(within(forms[1]).getByRole("checkbox", { name: "Mary" }));
  await userEvent.selectOptions(within(forms[1]).getByLabelText("Answer for Mary"), "maybe");

  await act(() => {
    within(forms[0]).getByRole("button", { name: "Save answers" }).click();
    within(forms[1]).getByRole("button", { name: "Save answers" }).click();
  });
  expect(Events.register).toHaveBeenCalledTimes(1);
  expect(within(forms[1]).getByRole("checkbox", { name: "Mary" })).toBeDisabled();
  expect(within(forms[1]).getByLabelText("Answer for Mary")).toHaveValue("maybe");

  await act(async () => pending.reject(new Error("offline")));
  await waitFor(() => expect(within(forms[1]).getByRole("checkbox", { name: "Mary" })).toBeEnabled());
  expect(within(forms[1]).getByRole("checkbox", { name: "Mary" })).toBeChecked();
  expect(within(forms[1]).getByLabelText("Answer for Mary")).toHaveValue("maybe");

  vi.mocked(Events.register).mockResolvedValue({ registration: null, people: [{ registrationId: 9, personId: 3, registeredBy: 2, status: "maybe", attendanceMode: "in_person", waitlistPosition: null, childcareRequested: false }] });
  await userEvent.click(within(forms[1]).getByRole("button", { name: "Save answers" }));
  expect(Events.register).toHaveBeenLastCalledWith(8, {
    date: "2026-12-08", people: [{ personId: 3, status: "maybe", attendanceMode: "in_person", childcareRequested: false }],
  });
});

it("opens the same series scope dialog from a group date for a solo viewer", async () => {
  vi.mocked(Households.mine).mockResolvedValue({ household: null });
  vi.mocked(Groups.get).mockResolvedValue({ ...page, upcomingEvents: [occurrence({ recurrenceRule: "FREQ=WEEKLY" })] });
  open();
  await userEvent.click(await screen.findByRole("button", { name: "Going" }));
  expect(screen.getByRole("dialog", { name: /How far should “Going” apply/ })).toBeInTheDocument();
});

it("keeps account-only series scope available on a group date with household people", async () => {
  vi.mocked(Groups.get).mockResolvedValue({ ...page, upcomingEvents: [occurrence({ recurrenceRule: "FREQ=WEEKLY" })] });
  vi.mocked(Events.registerSeries).mockResolvedValue({
    eventId: 7, scope: "this_and_following", status: "going",
    results: [{ occurrenceId: 1, date: "2026-12-01", before: {}, after: { registrationId: 4, status: "going" } }],
    refusals: [],
  });
  open();
  await userEvent.click(await screen.findByRole("checkbox", { name: "Me" }));
  await userEvent.click(screen.getByRole("checkbox", { name: "Mary" }));
  await userEvent.click(screen.getByRole("button", { name: "Choose dates for my answer" }));
  await userEvent.click(screen.getByRole("button", { name: "This and following dates" }));
  expect(Events.registerSeries).toHaveBeenCalledWith(7, expect.objectContaining({ scope: "this_and_following", date: "2026-12-01", status: "going" }));
  expect(screen.getByRole("checkbox", { name: "Mary" })).toBeChecked();
});

it("shows the authoritative series result on a group card while its parent GET is held", async () => {
  vi.mocked(Households.mine).mockResolvedValue({ household: null });
  const initial = { ...page, upcomingEvents: [occurrence({ recurrenceRule: "FREQ=WEEKLY", myRegistration: null })] };
  const held = deferred<GroupDetailResponse>();
  vi.mocked(Groups.get).mockResolvedValueOnce(initial).mockReturnValueOnce(held.promise);
  vi.mocked(Events.registerSeries).mockResolvedValue({
    eventId: 7, scope: "all_future", status: "waitlisted",
    results: [{ occurrenceId: 1, date: "2026-12-01", before: {}, after: {
      registrationId: 4, status: "waitlisted", waitlistPosition: 3, attendanceMode: "in_person",
      guestCount: 0, childcareCount: 0, childcareAgesNote: "",
    } }], refusals: [],
  });

  open();
  await userEvent.click(await screen.findByRole("button", { name: "Going to all dates" }));
  await waitFor(() => expect(Groups.get).toHaveBeenCalledTimes(2));
  expect(screen.getByRole("button", { name: "Going" })).toHaveAttribute("aria-pressed", "true");
  expect(screen.getByText(/Waitlisted — position 3/)).toBeInTheDocument();
  await act(async () => held.resolve({ ...initial, upcomingEvents: [occurrence({ recurrenceRule: "FREQ=WEEKLY",
    myRegistration: "waitlisted", myWaitlistPosition: 3, myAttendanceMode: "in_person", myGuestCount: 0 })] }));
  expect(screen.getByRole("button", { name: "Going" })).toHaveAttribute("aria-pressed", "true");
});

it("clears protected group data while a terminal Undo refresh is still pending", async () => {
  const recurring = { ...page, upcomingEvents: [occurrence({ recurrenceRule: "FREQ=WEEKLY" })] };
  const terminal = deferred<GroupDetailResponse>();
  vi.mocked(Groups.get)
    .mockResolvedValueOnce(recurring)
    .mockResolvedValueOnce(recurring)
    .mockReturnValueOnce(terminal.promise);
  vi.mocked(Events.registerSeries).mockResolvedValue({
    eventId: 7, scope: "all_future", status: "maybe",
    results: [{ occurrenceId: 1, date: "2026-12-01", before: {}, after: { registrationId: 4, status: "maybe" } }],
    refusals: [], undoToken: "undo-token", undoExpiresAt: new Date(Date.now() + 10_000).toISOString(),
  });
  vi.mocked(Events.undoSeriesRegistration).mockRejectedValue(new ApiError(409, "revoked", { code: "undo_authority_revoked" }));
  open();
  await userEvent.click(await screen.findByRole("button", { name: "Maybe for all" }));
  await userEvent.click(await screen.findByRole("button", { name: "Undo" }));
  await waitFor(() => expect(Groups.get).toHaveBeenCalledTimes(3));
  expect(screen.queryByRole("heading", { name: "Family group" })).not.toBeInTheDocument();
  await act(async () => terminal.reject(new ApiError(404, "hidden", { code: "not_found" })));
  expect(await screen.findByRole("heading", { name: "Group not found" })).toBeInTheDocument();
});

it("keeps the newest group refresh when an older quiet request resolves last", async () => {
  const recurring = { ...page, upcomingEvents: [occurrence({ recurrenceRule: "FREQ=WEEKLY" })] };
  const old = deferred<GroupDetailResponse>();
  const terminal = deferred<GroupDetailResponse>();
  const stale = { ...recurring, group: { ...recurring.group, name: "Stale group details" } };
  const current = { ...recurring, group: { ...recurring.group, name: "Current group details" } };
  vi.mocked(Groups.get)
    .mockResolvedValueOnce(recurring)
    .mockReturnValueOnce(old.promise)
    .mockReturnValueOnce(terminal.promise);
  vi.mocked(Events.registerSeries).mockResolvedValue({
    eventId: 7, scope: "all_future", status: "maybe",
    results: [{ occurrenceId: 1, date: "2026-12-01", before: {}, after: { registrationId: 4, status: "maybe" } }],
    refusals: [], undoToken: "undo-token", undoExpiresAt: new Date(Date.now() + 10_000).toISOString(),
  });
  vi.mocked(Events.undoSeriesRegistration).mockRejectedValue(new ApiError(409, "changed", { code: "undo_changed" }));
  open();
  await userEvent.click(await screen.findByRole("button", { name: "Maybe for all" }));
  await userEvent.click(await screen.findByRole("button", { name: "Undo" }));
  await waitFor(() => expect(Groups.get).toHaveBeenCalledTimes(3));
  expect(screen.queryByRole("heading", { name: "Family group" })).not.toBeInTheDocument();
  await act(async () => terminal.resolve(current));
  expect(await screen.findByRole("heading", { name: "Current group details" })).toBeInTheDocument();
  await act(async () => old.resolve(stale));
  expect(screen.getByRole("heading", { name: "Current group details" })).toBeInTheDocument();
  expect(screen.queryByRole("heading", { name: "Stale group details" })).not.toBeInTheDocument();
});

it.each([null, { id: 2, accountType: "teen", hideContactFromLeaders: true } as User])("does not fetch household data for an unsigned or teen viewer", async (user) => {
  actor.user = user; open(); await screen.findByRole("heading", { name: "Family group" });
  expect(Households.mine).not.toHaveBeenCalled();
});

it("shows supplied roster contact without reconstructing hidden members' details", async () => {
  const member = { avatarUrl: "", avatarFocalX: 50, avatarFocalY: 50, role: "member", joinedAt: "2026-10-01T00:00:00Z", admin: false };
  vi.mocked(Groups.get).mockResolvedValue({ ...page, members: [
    { ...member, id: 3, name: "Visible adult", email: "visible@example.com", phone: "208-555-0100" },
    { ...member, id: 4, name: "Hidden adult" },
    { ...member, id: 5, name: "Teen member", accountType: "teen" },
  ] });
  open();
  await userEvent.click(await screen.findByRole("button", { name: "Members" }));
  expect(screen.getByRole("link", { name: "visible@example.com" })).toBeInTheDocument();
  expect(screen.getByText("Phone: 208-555-0100")).toBeInTheDocument();
  expect(screen.getAllByRole("link", { name: /@/ })).toHaveLength(1);
  expect(screen.getAllByText(/@/)).toHaveLength(1); // no plain-text fallback for hidden contacts either
  expect(screen.queryByText(/Email:/)).not.toBeInTheDocument();
  expect(screen.getAllByText(/Phone:/)).toHaveLength(1);
  expect(screen.getByText("Hidden adult")).toBeInTheDocument();
  expect(screen.getByText("Teen member")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /contact/i })).not.toBeInTheDocument();
});

it.each(["group_full", "teen_add_approval_required", "people_selection_unavailable"])("adds by name and only retries the actual member limit: %s", async (code) => {
  actor.user = { id: 2, status: "active", accountType: "adult" } as User;
  vi.mocked(Groups.get).mockResolvedValue({ ...page, canManage: true });
  vi.mocked(People.search).mockResolvedValue({ state: "results", people: [{ selectionId: "opaque", displayName: "Pat", teen: false, sharedGroups: [], otherGroups: [] }] });
  vi.mocked(Groups.addMember).mockRejectedValueOnce(new ApiError(409, "private", { code })).mockResolvedValueOnce({});
  vi.spyOn(window, "confirm").mockReturnValue(true);
  open(); await userEvent.click(await screen.findByRole("button", { name: "Members" }));
  await userEvent.type(screen.getByRole("combobox"), "Pat"); await userEvent.click(await screen.findByRole("option"));
  expect(screen.getByLabelText("Add a member by email")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Add member" }));
  expect(Groups.addMember).toHaveBeenNthCalledWith(1, "1", { selectionId: "opaque" });
  if (code === "group_full") expect(Groups.addMember).toHaveBeenLastCalledWith("1", { selectionId: "opaque" }, true);
  else { expect(Groups.addMember).toHaveBeenCalledTimes(1); expect(screen.getByRole("alert")).not.toHaveTextContent("private"); }
});

it("offers an explicit Ask to add for a selected teen instead of direct add", async () => {
  actor.user = { id: 2, status: "active", accountType: "adult" } as User;
  vi.mocked(Groups.get).mockResolvedValue({ ...page, canManage: true });
  vi.mocked(People.search).mockResolvedValue({ state: "results", people: [{ selectionId: "teen", displayName: "Teen Pat", teen: true, sharedGroups: [], otherGroups: [] }] });
  open(); await userEvent.click(await screen.findByRole("button", { name: "Members" }));
  await userEvent.type(screen.getByRole("combobox"), "Pat"); await userEvent.click(await screen.findByRole("option"));
  expect(screen.getByRole("status")).toHaveTextContent("Adding a teen needs their approval");
  expect(screen.getByRole("button", { name: "Ask to add" })).toBeEnabled();
  await userEvent.click(screen.getByRole("button", { name: "Ask to add" }));
  expect(screen.getByRole("button", { name: "Send ask to add" })).toBeInTheDocument();
  expect(Groups.addMember).not.toHaveBeenCalled();
});

it("routes self-leave through preview and explicit snapshot confirmation", async () => {
  vi.mocked(Groups.get).mockResolvedValueOnce(page).mockResolvedValueOnce({ ...page, isMember: false });
  open();
  await userEvent.click(await screen.findByRole("button", { name: "Leave group" }));
  const dialog = await screen.findByRole("dialog", { name: "Leave Family group?" });
  expect(Groups.confirmDeparture).not.toHaveBeenCalled();
  await userEvent.click(within(dialog).getByRole("button", { name: "Leave group" }));
  expect(Groups.confirmDeparture).toHaveBeenCalledWith("1", "self", []);
  expect(await screen.findByText("0 dates kept · 0 dates ended.")).toHaveAttribute("role", "status");
});

it("does not let a deferred pre-leave refresh restore protected group authority", async () => {
  const leader = { id: 2, name: "Current Leader", email: "leader@example.com", phone: "208-555-0101", avatarUrl: "", avatarFocalX: 50, avatarFocalY: 50, role: "leader" as const, joinedAt: "2026-10-01T00:00:00Z", admin: false };
  const protectedMember = { ...leader, id: 4, name: "Protected Member", email: "protected@example.com", role: "member" as const };
  const privileged = { ...page, canManage: true, isMember: true, members: [leader, protectedMember] };
  const departed = { ...page, canManage: false, isMember: false, members: [], upcomingEvents: [] };
  const oldRefresh = deferred<GroupDetailResponse>();
  const postDeparture = deferred<GroupDetailResponse>();
  vi.mocked(Groups.get)
    .mockResolvedValueOnce(privileged)
    .mockReturnValueOnce(oldRefresh.promise)
    .mockReturnValueOnce(postDeparture.promise);
  vi.mocked(Events.register).mockResolvedValue({ registration: null, people: [{ registrationId: 9, personId: 3, registeredBy: 2, status: "going", attendanceMode: "in_person", waitlistPosition: null, childcareRequested: false }] });

  open();
  await userEvent.click(await screen.findByRole("checkbox", { name: "Mary" }));
  await userEvent.click(screen.getByRole("button", { name: "Save answers" }));
  await waitFor(() => expect(Groups.get).toHaveBeenCalledTimes(2));
  await userEvent.click(screen.getByRole("button", { name: "More group actions" }));
  await userEvent.click(screen.getByRole("menuitem", { name: "Leave group" }));
  await userEvent.click(within(await screen.findByRole("dialog", { name: "Leave Family group?" })).getByRole("button", { name: "Leave group" }));
  await waitFor(() => expect(Groups.get).toHaveBeenCalledTimes(3));
  expect(screen.queryByRole("heading", { name: "Family group" })).not.toBeInTheDocument();

  await act(async () => postDeparture.resolve(departed));
  expect(await screen.findByRole("button", { name: "Join this group" })).toBeInTheDocument();
  await act(async () => oldRefresh.resolve(privileged));
  expect(screen.getByRole("button", { name: "Join this group" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Leave group" })).not.toBeInTheDocument();
  expect(screen.queryByText("Protected Member")).not.toBeInTheDocument();
  expect(screen.queryByText("protected@example.com")).not.toBeInTheDocument();
});

it("does not let a stale post-leave rejection redirect a newer group view", async () => {
  const postDeparture = deferred<GroupDetailResponse>();
  const nextGroup = { ...page, group: { ...page.group, id: 2, name: "Newer group view" }, isMember: false, upcomingEvents: [] };
  vi.mocked(Groups.get)
    .mockResolvedValueOnce(page)
    .mockReturnValueOnce(postDeparture.promise)
    .mockResolvedValueOnce(nextGroup);

  openWithSwitcher();
  await userEvent.click(await screen.findByRole("button", { name: "Leave group" }));
  await userEvent.click(within(await screen.findByRole("dialog", { name: "Leave Family group?" })).getByRole("button", { name: "Leave group" }));
  await waitFor(() => expect(Groups.get).toHaveBeenCalledTimes(2));
  await userEvent.click(screen.getByRole("button", { name: "Open next group" }));
  expect(await screen.findByRole("heading", { name: "Newer group view" })).toBeInTheDocument();

  await act(async () => postDeparture.reject(new ApiError(404, "hidden", { code: "not_found" })));
  expect(screen.getByRole("heading", { name: "Newer group view" })).toBeInTheDocument();
  expect(screen.queryByRole("heading", { name: "My groups destination" })).not.toBeInTheDocument();
});

it("routes leader removal through the same chooser and preserves its optional note", async () => {
  const member = { id: 3, name: "Pat Person", avatarUrl: "", avatarFocalX: 50, avatarFocalY: 50, role: "member", joinedAt: "2026-10-01T00:00:00Z", admin: false };
  vi.mocked(Groups.get).mockResolvedValue({ ...page, canManage: true, members: [member] });
  vi.mocked(Groups.memberDeparturePreview).mockResolvedValue({ departure: { action: "remove", groupId: 1, groupName: "Family group", subjectId: 3, subjectName: "Pat Person", affectedCount: 0, goingCount: 0, maybeCount: 0, waitlistedCount: 0, keepableCount: 0, defaultKeepCount: 0, dates: [], snapshot: "remove" } });
  vi.mocked(Groups.confirmMemberDeparture).mockResolvedValue({ departure: { action: "remove", groupId: 1, subjectId: 3, keptCount: 0, endedCount: 0 } });
  open();
  await userEvent.click(await screen.findByRole("button", { name: "Members" }));
  await userEvent.click(screen.getByRole("button", { name: "More for Pat Person" }));
  await userEvent.click(screen.getByRole("menuitem", { name: "Remove from group" }));
  const dialog = await screen.findByRole("dialog", { name: "Remove Pat Person?" });
  await userEvent.type(within(dialog).getByLabelText("Note for church admins (optional)"), "Requested by pastor");
  await userEvent.click(within(dialog).getByRole("button", { name: "Remove from group" }));
  await waitFor(() => expect(Groups.confirmMemberDeparture).toHaveBeenCalledWith("1", 3, "remove", [], "Requested by pastor"));
});

it("keeps the date visibility note in the content column rather than another horizontal column", async () => {
  vi.mocked(Groups.get).mockResolvedValue({ ...page, canManage: true, upcomingEvents: [occurrence({ visibilityOverride: "public" })] });
  open();
  const note = await screen.findByText("Visibility changed for this date");
  expect(note.parentElement).toHaveClass("grow", "min-w-0");
  expect(note.parentElement).not.toHaveClass("flex");
});

it("links to the full group calendar when upcoming dates are truncated", async () => {
  const upcomingEvents = Array.from({ length: 25 }, (_, i) => occurrence({ id: i + 1, eventId: i + 1 }));
  vi.mocked(Groups.get).mockResolvedValue({ ...page, upcomingEvents, truncated: true });
  open();
  expect(await screen.findByText("25 dates loaded.")).toBeVisible();
  expect(screen.getByRole("link", { name: "See all dates in the calendar" })).toHaveAttribute("href", "/calendar?group=1");
});

it("does not show a truncation notice for a complete upcoming list", async () => {
  open();
  await screen.findByRole("heading", { name: "Family group" });
  expect(screen.queryByRole("link", { name: "See all dates in the calendar" })).not.toBeInTheDocument();
});

it("merges continuation pages without duplicate dates and counts only loaded series dates", async () => {
  const first = { ...page, upcomingEvents: [occurrence({ recurrenceRule: "FREQ=WEEKLY" })], truncated: true, hasMore: true, nextCursor: "page-two" };
  const second = { ...page, upcomingEvents: [occurrence({ recurrenceRule: "FREQ=WEEKLY" }), occurrence({ id: 2, date: "2026-12-08", recurrenceRule: "FREQ=WEEKLY" })] };
  vi.mocked(Groups.get).mockResolvedValueOnce(first).mockResolvedValueOnce(second);
  open();
  await userEvent.click(await screen.findByRole("button", { name: "Load more dates" }));
  expect(Groups.get).toHaveBeenLastCalledWith("1", { cursor: "page-two" });
  expect(await screen.findByRole("link", { name: "1 more date loaded" })).toHaveAttribute("href", "/events/7");
  expect(screen.queryByText(/next 90 days/)).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Load more dates" })).not.toBeInTheDocument();
  expect(screen.getAllByRole("form", { name: "Household answers" })).toHaveLength(1);
});

it("keeps loaded pages on continuation failure and retries without losing the cursor", async () => {
  const first = { ...page, truncated: true, hasMore: true, nextCursor: "retry-page" };
  vi.mocked(Groups.get).mockResolvedValueOnce(first).mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce(page);
  open();
  await userEvent.click(await screen.findByRole("button", { name: "Load more dates" }));
  expect(await screen.findByRole("alert")).toBeInTheDocument();
  expect(screen.getByRole("heading", { name: "Family group" })).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Load more dates" }));
  expect(Groups.get).toHaveBeenLastCalledWith("1", { cursor: "retry-page" });
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
});

it("refreshes every loaded page after an RSVP", async () => {
  const first = { ...page, hasMore: true, truncated: true, nextCursor: "next" };
  const second = { ...page, upcomingEvents: [occurrence({ id: 2, eventId: 8 })] };
  vi.mocked(Groups.get).mockResolvedValueOnce(first).mockResolvedValueOnce(second).mockResolvedValueOnce(first).mockResolvedValueOnce(second);
  vi.mocked(Events.register).mockResolvedValue({ registration: null, people: [] });
  open();
  await userEvent.click(await screen.findByRole("button", { name: "Load more dates" }));
  const forms = await screen.findAllByRole("form", { name: "Household answers" });
  expect(forms).toHaveLength(2);
  await userEvent.click(within(forms[0]).getByRole("checkbox", { name: "Mary" }));
  await userEvent.click(within(forms[0]).getByRole("button", { name: "Save answers" }));
  await waitFor(() => expect(Groups.get).toHaveBeenCalledTimes(4));
  expect(Groups.get).toHaveBeenNthCalledWith(3, "1");
  expect(Groups.get).toHaveBeenNthCalledWith(4, "1", { cursor: "next" });
  expect(screen.getAllByRole("form", { name: "Household answers" })).toHaveLength(2);
});

it("resets continuation on group change and ignores an old pending page", async () => {
  const pending = deferred<GroupDetailResponse>();
  const nextGroup = { ...page, group: { ...page.group, id: 2, name: "Next group" }, upcomingEvents: [] };
  vi.mocked(Groups.get).mockResolvedValueOnce({ ...page, hasMore: true, truncated: true, nextCursor: "old" }).mockReturnValueOnce(pending.promise).mockResolvedValueOnce(nextGroup);
  openWithSwitcher();
  await userEvent.click(await screen.findByRole("button", { name: "Load more dates" }));
  expect(screen.getByRole("button", { name: "Loading dates…" })).toBeDisabled();
  await userEvent.click(screen.getByRole("button", { name: "Open next group" }));
  expect(await screen.findByRole("heading", { name: "Next group" })).toBeInTheDocument();
  expect(Groups.get).toHaveBeenLastCalledWith("2");
  await act(async () => pending.resolve(page));
  expect(screen.getByRole("heading", { name: "Next group" })).toBeInTheDocument();
  expect(screen.queryByRole("form", { name: "Household answers" })).not.toBeInTheDocument();
});

it("clears dates and cursor immediately when the viewer changes", async () => {
  const pending = deferred<GroupDetailResponse>();
  const publicPage = deferred<GroupDetailResponse>();
  vi.mocked(Groups.get).mockResolvedValueOnce({ ...page, hasMore: true, truncated: true, nextCursor: "member-only" }).mockReturnValueOnce(pending.promise).mockReturnValueOnce(publicPage.promise);
  const view = () => <MemoryRouter initialEntries={["/groups/1"]}><Routes><Route path="/groups/:id" element={<GroupDetail />} /></Routes></MemoryRouter>;
  const rendered = render(view());
  await userEvent.click(await screen.findByRole("button", { name: "Load more dates" }));
  actor.user = null;
  rendered.rerender(view());
  await waitFor(() => expect(Groups.get).toHaveBeenCalledTimes(3));
  expect(Groups.get).toHaveBeenLastCalledWith("1");
  expect(screen.queryByRole("heading", { name: "Family group" })).not.toBeInTheDocument();
  await act(async () => pending.resolve(page));
  expect(screen.queryByRole("heading", { name: "Family group" })).not.toBeInTheDocument();
  await act(async () => publicPage.resolve({ ...page, isMember: false, upcomingEvents: [] }));
  expect(await screen.findByRole("heading", { name: "Family group" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Load more dates" })).not.toBeInTheDocument();
});

it("keeps loading a series beyond 100 dates using the latest page metadata", async () => {
  const dates = Array.from({ length: 126 }, (_, i) => occurrence({ id: i + 1, recurrenceRule: "FREQ=DAILY" }));
  vi.mocked(Groups.get)
    .mockResolvedValueOnce({ ...page, upcomingEvents: dates.slice(0, 25), truncated: true, hasMore: true, nextCursor: "after-25" })
    .mockResolvedValueOnce({ ...page, upcomingEvents: dates.slice(24, 100), truncated: true, hasMore: true, nextCursor: "after-100" })
    .mockResolvedValueOnce({ ...page, upcomingEvents: dates.slice(100) });
  open();
  await userEvent.click(await screen.findByRole("button", { name: "Load more dates" }));
  expect(await screen.findByText("100 dates loaded.")).toBeVisible();
  expect(screen.getByRole("link", { name: "99 more dates loaded" })).toBeVisible();
  await userEvent.click(screen.getByRole("button", { name: "Load more dates" }));
  expect(Groups.get).toHaveBeenLastCalledWith("1", { cursor: "after-100" });
  expect(await screen.findByRole("link", { name: "125 more dates loaded" })).toBeVisible();
  expect(screen.queryByRole("button", { name: "Load more dates" })).not.toBeInTheDocument();
  expect(screen.queryByText("100 dates loaded.")).not.toBeInTheDocument();
  expect(screen.getAllByRole("form", { name: "Household answers" })).toHaveLength(1);
});
