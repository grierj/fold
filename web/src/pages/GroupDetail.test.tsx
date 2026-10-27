import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { ApiError, Events, Groups, Households, People } from "../api";
import type { GroupDetailResponse, Household, User } from "../types";
import { occurrence } from "../test/occurrence";
import { GroupDetail } from "./GroupDetail";

const actor = vi.hoisted(() => ({ user: { id: 2, accountType: "adult", hideContactFromLeaders: false } as User | null }));
vi.mock("../auth", () => ({ useAuth: () => ({ user: actor.user, loading: false, isAdmin: false }) }));
vi.mock("../api", async (actual) => ({ ...(await actual<typeof import("../api")>()), People: { search: vi.fn() }, Groups: { get: vi.fn(), addMember: vi.fn() }, Events: { register: vi.fn(), registerSeries: vi.fn(), undoSeriesRegistration: vi.fn() }, Households: { mine: vi.fn() } }));
const family: Household = { id: 1, name: "Family", address: "", createdBy: 2, accounts: [], people: [{ id: 3, householdId: 1, canConvert: false, firstName: "Mary", lastName: "", name: "Mary", kind: "adult", photoConsent: false, linkStatus: "none", registrationEligibility: "eligible", createdBy: 2 }] };
const page: GroupDetailResponse = { group: { id: 1, name: "Family group", description: "", tags: [], imageUrl: "", coverFocalX: 50, coverFocalY: 50, location: "Hall", leaderName: "Leader", memberCount: 1, nextEventAt: null, status: "active", leaderId: 1, visibility: "public", deletionRequested: false, memberLimit: null }, members: [], upcomingEvents: [occurrence({})], canManage: false, isMember: true };
function open() { render(<MemoryRouter initialEntries={["/groups/1"]}><Routes><Route path="/groups/:id" element={<GroupDetail />} /></Routes></MemoryRouter>); }
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((accept, decline) => { resolve = accept; reject = decline; });
  return { promise, resolve, reject };
}
beforeEach(() => { vi.resetAllMocks(); actor.user = { id: 2, accountType: "adult", hideContactFromLeaders: false } as User; vi.mocked(Groups.get).mockResolvedValue(page); vi.mocked(Households.mine).mockResolvedValue({ household: family }); });

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
  await userEvent.click(await screen.findByRole("button", { name: "members" }));
  expect(screen.getByText("Email: visible@example.com")).toBeInTheDocument();
  expect(screen.getByText("Phone: 208-555-0100")).toBeInTheDocument();
  expect(screen.getAllByText(/Email:/)).toHaveLength(1);
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
  open(); await userEvent.click(await screen.findByRole("button", { name: "members" }));
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
  open(); await userEvent.click(await screen.findByRole("button", { name: "members" }));
  await userEvent.type(screen.getByRole("combobox"), "Pat"); await userEvent.click(await screen.findByRole("option"));
  expect(screen.getByRole("status")).toHaveTextContent("Adding a teen needs their approval");
  expect(screen.getByRole("button", { name: "Ask to add" })).toBeEnabled();
  await userEvent.click(screen.getByRole("button", { name: "Ask to add" }));
  expect(screen.getByRole("button", { name: "Send ask to add" })).toBeInTheDocument();
  expect(Groups.addMember).not.toHaveBeenCalled();
});

it("keeps the date visibility note in the content column rather than another horizontal column", async () => {
  vi.mocked(Groups.get).mockResolvedValue({ ...page, canManage: true, upcomingEvents: [occurrence({ visibilityOverride: "public" })] });
  open();
  const note = await screen.findByText("Visibility changed for this date");
  expect(note.parentElement).toHaveClass("grow", "min-w-0");
  expect(note.parentElement).not.toHaveClass("flex");
});
