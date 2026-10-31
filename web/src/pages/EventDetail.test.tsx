import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, RouterProvider, Routes, createMemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, DateRooms, Events, Households, SeriesRooms, Site } from "../api";
import type { EventDetailResponse, NeedsRoom } from "../types";
import { occurrence } from "../test/occurrence";
import { EventDetail } from "./EventDetail";

const composer = vi.hoisted(() => ({ on: false }));
vi.mock("../auth", () => ({ useAuth: () => ({ user: { id: 2 }, loading: false, isAdmin: false, canCompose: composer.on }) }));
vi.mock("../api", async (importActual) => ({
  ...(await importActual<typeof import("../api")>()),
  ApiError: (await importActual<typeof import("../api")>()).ApiError,
  Households: { mine: vi.fn().mockResolvedValue({ household: null }) },
  Events: { get: vi.fn(), updateOccurrence: vi.fn(), register: vi.fn(), registerSeries: vi.fn(), undoSeriesRegistration: vi.fn() },
}));

const page: EventDetailResponse = {
  event: {
    id: 7, title: "Hybrid study", description: "", startsLocal: "2026-12-01T19:00", durationMinutes: 60,
    tzid: "UTC", startsAt: "2026-12-01T19:00:00Z", endsAt: "2026-12-01T20:00:00Z", location: "Hall",
    recurrenceRule: "", visibility: "public", leaderOpened: false, churchWide: false,
    capacity: null, waitlistEnabled: false, guestsAllowed: 0, reminderEmailMinutes: 1440,
    childcareAvailable: false, childcareCapacity: null, childcareAges: "", childcareNotes: "",
    remoteAvailable: true, remoteCapacity: 5, videoProvider: "none", videoReveal: "registered_only",
  },
  occurrences: [occurrence({
    remoteAvailable: true, remoteCapacity: 5, remoteSpotsLeft: 4,
    attendeesVisible: true,
    attendees: [{ registrationId: 31, userId: 3, name: "Sam", status: "going", guestCount: 0, attendanceMode: "remote" }],
  })],
  canManage: true, canSetVisibility: false,
};

function renderPage(path = "/events/7") {
  render(<MemoryRouter initialEntries={[path]}><Routes><Route path="/events/:id" element={<EventDetail />} /></Routes></MemoryRouter>);
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((accept, decline) => { resolve = accept; reject = decline; });
  return { promise, resolve, reject };
}

// A series date has no whole-series buttons (DEC-92): its answer opens the
// scope dialog, and "All future dates" answers every date.
async function answerAll(group: HTMLElement, name: "Going" | "Maybe" | "Can't go") {
  await userEvent.click(within(group).getByRole("button", { name }));
  await userEvent.click(screen.getByRole("button", { name: "All future dates" }));
}
async function chooseDatesForMe(form: HTMLElement) {
  await userEvent.click(within(form).getByRole("button", { name: "Choose dates for my answer" }));
  await userEvent.click(screen.getByRole("button", { name: "All future dates" }));
}

async function editDate() {
  renderPage();
  await userEvent.click(await screen.findByRole("button", { name: "More for 2026-12-01" }));
  await userEvent.click(screen.getByRole("menuitem", { name: "Edit this date" }));
  return screen.getByRole("textbox", { name: /^Remote seat limit for this date/ });
}

it("wraps a long unbroken header location", async () => {
  const location = "L".repeat(160);
  vi.mocked(Events.get).mockResolvedValue({ ...page, event: { ...page.event, location } });
  renderPage();
  expect(await screen.findByText(location)).toHaveClass("wrap-anywhere");
});

describe("EventDetail remote seats", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    vi.mocked(Households.mine).mockResolvedValue({ household: null });
    vi.mocked(Events.get).mockResolvedValue(page);
    vi.mocked(Events.updateOccurrence).mockResolvedValue({ occurrence: page.occurrences[0] });
  });

  it.each(["1e", "-2", "1.5"])("preserves and focuses invalid per-date text %s without saving", async (text) => {
    const input = await editDate();
    expect(input).toHaveAttribute("inputmode", "numeric");
    await userEvent.clear(input);
    await userEvent.type(input, text);
    await userEvent.click(screen.getByRole("button", { name: "Save this date" }));
    expect(Events.updateOccurrence).not.toHaveBeenCalled();
    expect(input).toHaveValue(text);
    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(input).toHaveAccessibleDescription(expect.stringContaining("whole number from 0"));
    await waitFor(() => expect(input).toHaveFocus());
  });

  it.each([["0", 0], ["8", 8], ["", null]] as const)("saves the per-date limit %s", async (text, limit) => {
    const input = await editDate();
    await userEvent.clear(input);
    if (text) await userEvent.type(input, text);
    await userEvent.click(screen.getByRole("button", { name: "Save this date" }));
    expect(Events.updateOccurrence).toHaveBeenCalledWith(7, "2026-12-01", { remoteCapacityOverride: limit });
  });

  it("marks remote people in the leader roster", async () => {
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "More for 2026-12-01" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Manage sign-ups" }));
    const roster = within(await screen.findByRole("region", { name: "Manage sign-ups" }));
    expect(roster.getByText("Sam", { selector: "span:not(.sr-only)" })).toBeInTheDocument();
    expect(roster.getByText("Remote")).toHaveClass("bg-sunken", "text-text-muted");
  });
});

it("uses the shared scope dialog for a recurring date", async () => {
  vi.mocked(Events.get).mockResolvedValue({ ...page, event: { ...page.event, recurrenceRule: "FREQ=WEEKLY" }, occurrences: [occurrence({ recurrenceRule: "FREQ=WEEKLY" })] });
  renderPage();
  await userEvent.click((await screen.findAllByRole("button", { name: "Maybe" }))[0]);
  expect(screen.getByRole("dialog", { name: /How far should “Maybe” apply/ })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Just this date" })).toHaveFocus();
});

it("serializes sibling series actions synchronously and releases after rejection or cancelled confirmation", async () => {
  const dates = [occurrence({ recurrenceRule: "FREQ=WEEKLY" }), occurrence({ id: 2, date: "2026-12-08", recurrenceRule: "FREQ=WEEKLY" })];
  vi.mocked(Events.get).mockResolvedValue({ ...page, event: { ...page.event, recurrenceRule: "FREQ=WEEKLY" }, occurrences: dates });
  const pending = deferred<Awaited<ReturnType<typeof Events.registerSeries>>>();
  vi.mocked(Events.registerSeries).mockReturnValue(pending.promise);

  renderPage();
  const answerGroups = await screen.findAllByRole("group", { name: "Your answer" });
  await userEvent.click(within(answerGroups[0]).getByRole("button", { name: "Maybe" }));
  await userEvent.click(within(answerGroups[1]).getByRole("button", { name: "Maybe" }));
  const allFuture = screen.getAllByRole("button", { name: "All future dates" });
  await act(() => {
    allFuture[0].click();
    allFuture[1].click();
  });
  expect(Events.registerSeries).toHaveBeenCalledTimes(1);
  expect(within(answerGroups[1]).getByRole("button", { name: "Going" })).toBeDisabled();

  await act(async () => pending.reject(new Error("offline")));
  await waitFor(() => expect(within(answerGroups[1]).getByRole("button", { name: "Going" })).toBeEnabled());

  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  await answerAll(answerGroups[0], "Can't go");
  expect(confirm).toHaveBeenCalledOnce();
  expect(Events.registerSeries).toHaveBeenCalledTimes(1);
  expect(within(answerGroups[1]).getByRole("button", { name: "Maybe" })).toBeEnabled();

  vi.mocked(Events.registerSeries).mockResolvedValue({
    eventId: 7, scope: "all_future", status: "maybe",
    results: dates.map((row) => ({ occurrenceId: row.id, date: row.date, before: {}, after: { registrationId: row.id + 10, status: "maybe" as const } })),
    refusals: [],
  });
  await answerAll(answerGroups[1], "Maybe");
  expect(Events.registerSeries).toHaveBeenCalledTimes(2);
});

it("blocks sibling date and Undo mutations while their requests are pending, then releases the page gate", async () => {
  const dates = [occurrence({ recurrenceRule: "FREQ=WEEKLY" }), occurrence({ id: 2, date: "2026-12-08", recurrenceRule: "FREQ=WEEKLY" })];
  vi.mocked(Events.get).mockResolvedValue({ ...page, event: { ...page.event, recurrenceRule: "FREQ=WEEKLY" }, occurrences: dates });
  const datePending = deferred<Awaited<ReturnType<typeof Events.register>>>();
  vi.mocked(Events.register).mockReturnValueOnce(datePending.promise);
  vi.mocked(Events.registerSeries).mockResolvedValue({
    eventId: 7, scope: "all_future", status: "maybe",
    results: dates.map((row) => ({ occurrenceId: row.id, date: row.date, before: {}, after: { registrationId: row.id + 10, status: "maybe" as const } })),
    refusals: [], undoToken: "undo-token", undoExpiresAt: new Date(Date.now() + 10_000).toISOString(),
  });
  const undoPending = deferred<Awaited<ReturnType<typeof Events.undoSeriesRegistration>>>();
  vi.mocked(Events.undoSeriesRegistration).mockReturnValue(undoPending.promise);

  renderPage();
  const answerGroups = await screen.findAllByRole("group", { name: "Your answer" });
  await userEvent.click(within(answerGroups[0]).getByRole("button", { name: "Maybe" }));
  await userEvent.click(screen.getByRole("button", { name: "Just this date" }));
  expect(within(answerGroups[1]).getByRole("button", { name: "Going" })).toBeDisabled();
  expect(within(answerGroups[1]).getByRole("button", { name: "Maybe" })).toBeDisabled();
  await act(async () => datePending.reject(new Error("offline")));
  await waitFor(() => expect(within(answerGroups[1]).getByRole("button", { name: "Going" })).toBeEnabled());

  await answerAll(answerGroups[0], "Maybe");
  const undo = await screen.findByRole("button", { name: "Undo" });
  await userEvent.click(undo);
  expect(within(answerGroups[1]).getByRole("button", { name: "Going" })).toBeDisabled();
  await act(async () => undoPending.reject(new Error("offline")));
  await waitFor(() => expect(within(answerGroups[1]).getByRole("button", { name: "Going" })).toBeEnabled());
});

it("clears a sibling card's series Undo after a one-date answer", async () => {
  const dates = [occurrence({ recurrenceRule: "FREQ=WEEKLY" }), occurrence({ id: 2, date: "2026-12-08", recurrenceRule: "FREQ=WEEKLY" })];
  vi.mocked(Events.get).mockResolvedValue({ ...page, event: { ...page.event, recurrenceRule: "FREQ=WEEKLY" }, occurrences: dates });
  vi.mocked(Events.registerSeries).mockResolvedValue({
    eventId: 7, scope: "all_future", status: "maybe",
    results: dates.map((row) => ({ occurrenceId: row.id, date: row.date, before: {}, after: { registrationId: row.id + 10, status: "maybe" as const } })),
    refusals: [], undoToken: "undo-token", undoExpiresAt: new Date(Date.now() + 10_000).toISOString(),
  });
  vi.mocked(Events.register).mockResolvedValue({ registration: {
    registrationId: 12, userId: 2, eventId: 7, occurrenceId: 2, date: "2026-12-08", status: "going",
    guestCount: 0, childcareCount: 0, childcareAgesNote: "", waitlistPosition: null, attendanceMode: "in_person",
  }, people: [] });
  renderPage();
  const answerGroups = await screen.findAllByRole("group", { name: "Your answer" });
  await answerAll(answerGroups[0], "Maybe");
  expect(await screen.findByRole("button", { name: "Undo" })).toBeInTheDocument();
  await userEvent.click(within(answerGroups[1]).getByRole("button", { name: "Going" }));
  await userEvent.click(screen.getByRole("button", { name: "Just this date" }));
  await waitFor(() => expect(screen.queryByRole("button", { name: "Undo" })).not.toBeInTheDocument());
});

it("uses series Apply results before its parent GET and rejects that older GET after a sibling save", async () => {
  const first = occurrence({ recurrenceRule: "FREQ=WEEKLY", remoteAvailable: true, remoteCapacity: 10,
    childcareAvailable: true, guestsAllowed: 2, myRegistration: "going", myAttendanceMode: "remote",
    myGuestCount: 2, myChildcareCount: 2, myChildcareAgesNote: "4 and 6" });
  const second = occurrence({ id: 2, date: "2026-12-08", recurrenceRule: "FREQ=WEEKLY", remoteAvailable: true,
    remoteCapacity: 10, childcareAvailable: true, guestsAllowed: 2, myRegistration: "going",
    myAttendanceMode: "in_person", myGuestCount: 0, myChildcareCount: 0, myChildcareAgesNote: "" });
  const stalePage = { ...page, event: { ...page.event, recurrenceRule: "FREQ=WEEKLY", childcareAvailable: true,
    guestsAllowed: 2, remoteAvailable: true, remoteCapacity: 10 }, occurrences: [first, second] };
  const appliedRows = [first, second].map((row) => ({ ...row, myAttendanceMode: "remote" as const,
    myGuestCount: 2, myChildcareCount: 2, myChildcareAgesNote: "4 and 6" }));
  const appliedPage = { ...stalePage, occurrences: appliedRows };
  const held = deferred<EventDetailResponse>();
  vi.mocked(Events.get).mockResolvedValueOnce(stalePage).mockReturnValueOnce(held.promise).mockResolvedValue(appliedPage);
  vi.mocked(Events.registerSeries).mockResolvedValue({
    eventId: 7, scope: "all_future", status: "going",
    results: appliedRows.map((row) => ({ occurrenceId: row.id, date: row.date, before: {}, after: {
      registrationId: row.id + 10, status: "going", attendanceMode: "remote", guestCount: 2,
      childcareCount: 2, childcareAgesNote: "4 and 6",
    } })), refusals: [],
  });
  vi.mocked(Events.register).mockResolvedValue({ registration: {
    registrationId: 12, userId: 2, eventId: 7, occurrenceId: 2, date: second.date, status: "going",
    guestCount: 2, childcareCount: 2, childcareAgesNote: "4 and 6", waitlistPosition: null, attendanceMode: "remote",
  }, people: [] });

  renderPage();
  const cards = await screen.findAllByRole("group", { name: "Your answer" });
  await answerAll(cards[0], "Going");
  await waitFor(() => expect(Events.get).toHaveBeenCalledTimes(2));
  const sibling = within(cards[1].parentElement!.parentElement!);
  expect(sibling.getByRole("radio", { name: "Remote" })).toBeChecked();
  expect(sibling.getByLabelText("Extra guests")).toHaveValue(2);
  // Childcare is folded into one summary line until changed (DEC-92).
  expect(sibling.getByText(/Childcare for 2 \(ages 4 and 6\)/)).toBeInTheDocument();
  await userEvent.click(sibling.getByRole("button", { name: "Change childcare request" }));
  expect(sibling.getByLabelText(/^Children needing childcare/)).toHaveValue("2");
  expect(sibling.getByLabelText(/^Ages/)).toHaveValue("4 and 6");

  await userEvent.click(sibling.getByRole("button", { name: "Save childcare" }));
  await userEvent.click(screen.getByRole("button", { name: "Just this date" }));
  expect(Events.register).toHaveBeenLastCalledWith(7, {
    status: "going", date: second.date, guestCount: 2, attendanceMode: "remote",
    childcareCount: 2, childcareAgesNote: "4 and 6",
  });
  await waitFor(() => expect(Events.get).toHaveBeenCalledTimes(3));
  await act(async () => held.resolve(stalePage));
  expect(sibling.getByRole("radio", { name: "Remote" })).toBeChecked();
  expect(sibling.getByLabelText("Extra guests")).toHaveValue(2);
});

it("maps series Undo to not answered before its parent GET and cannot stale-save applied values", async () => {
  const appliedRows = [
    occurrence({ recurrenceRule: "FREQ=WEEKLY", remoteAvailable: true, remoteCapacity: 10, childcareAvailable: true,
      guestsAllowed: 2, myRegistration: "going", myAttendanceMode: "remote", myGuestCount: 2,
      myChildcareCount: 2, myChildcareAgesNote: "4 and 6" }),
    occurrence({ id: 2, date: "2026-12-08", recurrenceRule: "FREQ=WEEKLY", remoteAvailable: true,
      remoteCapacity: 10, childcareAvailable: true, guestsAllowed: 2, myRegistration: "going",
      myAttendanceMode: "remote", myGuestCount: 2, myChildcareCount: 2, myChildcareAgesNote: "4 and 6" }),
  ];
  const appliedPage = { ...page, event: { ...page.event, recurrenceRule: "FREQ=WEEKLY", childcareAvailable: true,
    guestsAllowed: 2, remoteAvailable: true, remoteCapacity: 10 }, occurrences: appliedRows };
  const maybeRows = appliedRows.map((row, index) => index === 1 ? ({ ...row, myRegistration: "maybe" as const,
    myAttendanceMode: "in_person" as const, myGuestCount: 0, myChildcareCount: 0, myChildcareAgesNote: "" }) : row);
  const held = deferred<EventDetailResponse>();
  vi.mocked(Events.get).mockResolvedValueOnce(appliedPage).mockResolvedValueOnce(appliedPage)
    .mockReturnValueOnce(held.promise).mockResolvedValue({ ...appliedPage, occurrences: maybeRows });
  vi.mocked(Events.registerSeries).mockResolvedValue({
    eventId: 7, scope: "all_future", status: "going",
    results: appliedRows.map((row) => ({ occurrenceId: row.id, date: row.date, before: {}, after: {
      registrationId: row.id + 10, status: "going", attendanceMode: "remote", guestCount: 2,
      childcareCount: 2, childcareAgesNote: "4 and 6",
    } })), refusals: [], undoToken: "undo-token", undoExpiresAt: new Date(Date.now() + 10_000).toISOString(),
  });
  vi.mocked(Events.undoSeriesRegistration).mockResolvedValue({
    eventId: 7, scope: "", status: "", results: appliedRows.map((row) => ({
      occurrenceId: row.id, date: row.date, before: { status: "going" as const }, after: {},
    })), refusals: [],
  });
  vi.mocked(Events.register).mockResolvedValue({ registration: {
    registrationId: 12, userId: 2, eventId: 7, occurrenceId: 2, date: appliedRows[1].date, status: "maybe",
    guestCount: 0, childcareCount: 0, childcareAgesNote: "", waitlistPosition: null, attendanceMode: "in_person",
  }, people: [] });

  renderPage();
  const cards = await screen.findAllByRole("group", { name: "Your answer" });
  const first = within(cards[0].parentElement!.parentElement!);
  const sibling = within(cards[1].parentElement!.parentElement!);
  await answerAll(cards[0], "Going");
  await userEvent.click(await first.findByRole("button", { name: "Undo" }));
  await waitFor(() => expect(Events.get).toHaveBeenCalledTimes(3));
  for (const name of ["Going", "Maybe", "Can't go"]) {
    expect(sibling.getByRole("button", { name })).toHaveAttribute("aria-pressed", "false");
  }
  expect(sibling.queryByLabelText(/^Children needing childcare/)).not.toBeInTheDocument();

  await userEvent.click(sibling.getByRole("button", { name: "Maybe" }));
  await userEvent.click(screen.getByRole("button", { name: "Just this date" }));
  expect(Events.register).toHaveBeenLastCalledWith(7, {
    status: "maybe", date: appliedRows[1].date, guestCount: 0, attendanceMode: "in_person",
  });
  await waitFor(() => expect(Events.get).toHaveBeenCalledTimes(4));
  await act(async () => held.resolve(appliedPage));
  expect(sibling.getByRole("button", { name: "Maybe" })).toHaveAttribute("aria-pressed", "true");
  expect(sibling.getByRole("radio", { name: "In person" })).toBeChecked();
});

it("uses series Apply results in household forms while the parent GET is held and preserves proxy drafts", async () => {
  vi.mocked(Households.mine).mockResolvedValue({ household: { id: 1, name: "Family", address: "", createdBy: 2,
    accounts: [], people: [{ id: 3, householdId: 1, canConvert: false, firstName: "Mary", lastName: "", name: "Mary",
      kind: "adult", photoConsent: false, linkStatus: "none", registrationEligibility: "eligible", createdBy: 2 }] } });
  const first = occurrence({ recurrenceRule: "FREQ=WEEKLY", remoteAvailable: true, remoteCapacity: 10,
    childcareAvailable: true, guestsAllowed: 2, myRegistration: "going", myAttendanceMode: "remote",
    myGuestCount: 2, myChildcareCount: 2, myChildcareAgesNote: "4 and 6" });
  const second = occurrence({ id: 2, date: "2026-12-08", recurrenceRule: "FREQ=WEEKLY", remoteAvailable: true,
    remoteCapacity: 10, childcareAvailable: true, guestsAllowed: 2, myRegistration: "going",
    myAttendanceMode: "in_person", myGuestCount: 0, myChildcareCount: 0, myChildcareAgesNote: "" });
  const stalePage = { ...page, event: { ...page.event, recurrenceRule: "FREQ=WEEKLY", childcareAvailable: true,
    guestsAllowed: 2, remoteAvailable: true, remoteCapacity: 10 }, occurrences: [first, second] };
  const appliedRows = [first, second].map((row) => ({ ...row, myAttendanceMode: "remote" as const,
    myGuestCount: 2, myChildcareCount: 2, myChildcareAgesNote: "4 and 6" }));
  const appliedPage = { ...stalePage, occurrences: appliedRows };
  const held = deferred<EventDetailResponse>();
  vi.mocked(Events.get).mockResolvedValueOnce(stalePage).mockReturnValueOnce(held.promise).mockResolvedValue(appliedPage);
  vi.mocked(Events.registerSeries).mockResolvedValue({
    eventId: 7, scope: "all_future", status: "going",
    results: appliedRows.map((row) => ({ occurrenceId: row.id, date: row.date, before: {}, after: {
      registrationId: row.id + 10, status: "going", attendanceMode: "remote", guestCount: 2,
      childcareCount: 2, childcareAgesNote: "4 and 6",
    } })), refusals: [],
  });
  vi.mocked(Events.register).mockResolvedValue({ registration: {
    registrationId: 12, userId: 2, eventId: 7, occurrenceId: 2, date: second.date, status: "going",
    guestCount: 2, childcareCount: 2, childcareAgesNote: "4 and 6", waitlistPosition: null, attendanceMode: "remote",
  }, people: [] });

  renderPage();
  const forms = await screen.findAllByRole("form", { name: "Household answers" });
  const firstForm = within(forms[0]);
  const sibling = within(forms[1]);
  await userEvent.click(firstForm.getByRole("checkbox", { name: "Mary" }));
  await userEvent.selectOptions(firstForm.getByLabelText("Answer for Mary"), "maybe");
  await chooseDatesForMe(forms[0]);
  await waitFor(() => expect(Events.get).toHaveBeenCalledTimes(2));
  expect(within(sibling.getByRole("group", { name: "How you will attend" })).getByRole("radio", { name: "Remote" })).toBeChecked();
  expect(sibling.getByLabelText("Extra guests")).toHaveValue(2);
  expect(sibling.getByLabelText(/^Other children needing childcare/)).toHaveValue("2");
  expect(sibling.getByRole("textbox", { name: /^Ages/ })).toHaveValue("4 and 6");
  expect(firstForm.getByRole("checkbox", { name: "Mary" })).toBeChecked();
  expect(firstForm.getByLabelText("Answer for Mary")).toHaveValue("maybe");

  await userEvent.click(sibling.getByRole("button", { name: "Save answers" }));
  expect(Events.register).toHaveBeenLastCalledWith(7, {
    date: second.date, people: [], status: "going", attendanceMode: "remote", guestCount: 2,
    childcareCount: 2, childcareAgesNote: "4 and 6",
  });
  await waitFor(() => expect(Events.get).toHaveBeenCalledTimes(3));
  await act(async () => held.resolve(stalePage));
  expect(within(sibling.getByRole("group", { name: "How you will attend" })).getByRole("radio", { name: "Remote" })).toBeChecked();
  expect(firstForm.getByRole("checkbox", { name: "Mary" })).toBeChecked();
});

it("maps series Undo to no answer in household forms before the parent GET and saves fresh defaults", async () => {
  vi.mocked(Households.mine).mockResolvedValue({ household: { id: 1, name: "Family", address: "", createdBy: 2,
    accounts: [], people: [{ id: 3, householdId: 1, canConvert: false, firstName: "Mary", lastName: "", name: "Mary",
      kind: "adult", photoConsent: false, linkStatus: "none", registrationEligibility: "eligible", createdBy: 2 }] } });
  const appliedRows = [
    occurrence({ recurrenceRule: "FREQ=WEEKLY", remoteAvailable: true, remoteCapacity: 10, childcareAvailable: true,
      guestsAllowed: 2, myRegistration: "going", myAttendanceMode: "remote", myGuestCount: 2,
      myChildcareCount: 2, myChildcareAgesNote: "4 and 6" }),
    occurrence({ id: 2, date: "2026-12-08", recurrenceRule: "FREQ=WEEKLY", remoteAvailable: true,
      remoteCapacity: 10, childcareAvailable: true, guestsAllowed: 2, myRegistration: "going",
      myAttendanceMode: "remote", myGuestCount: 2, myChildcareCount: 2, myChildcareAgesNote: "4 and 6" }),
  ];
  const appliedPage = { ...page, event: { ...page.event, recurrenceRule: "FREQ=WEEKLY", childcareAvailable: true,
    guestsAllowed: 2, remoteAvailable: true, remoteCapacity: 10 }, occurrences: appliedRows };
  const savedRows = appliedRows.map((row, index) => index === 1 ? ({ ...row, myRegistration: "going" as const,
    myAttendanceMode: "in_person" as const, myGuestCount: 0, myChildcareCount: 0, myChildcareAgesNote: "" }) : row);
  const held = deferred<EventDetailResponse>();
  vi.mocked(Events.get).mockResolvedValueOnce(appliedPage).mockResolvedValueOnce(appliedPage)
    .mockReturnValueOnce(held.promise).mockResolvedValue({ ...appliedPage, occurrences: savedRows });
  vi.mocked(Events.registerSeries).mockResolvedValue({
    eventId: 7, scope: "all_future", status: "going",
    results: appliedRows.map((row) => ({ occurrenceId: row.id, date: row.date, before: {}, after: {
      registrationId: row.id + 10, status: "going", attendanceMode: "remote", guestCount: 2,
      childcareCount: 2, childcareAgesNote: "4 and 6",
    } })), refusals: [], undoToken: "undo-token", undoExpiresAt: new Date(Date.now() + 10_000).toISOString(),
  });
  vi.mocked(Events.undoSeriesRegistration).mockResolvedValue({
    eventId: 7, scope: "", status: "", results: appliedRows.map((row) => ({
      occurrenceId: row.id, date: row.date, before: { status: "going" as const }, after: {},
    })), refusals: [],
  });
  vi.mocked(Events.register).mockResolvedValue({ registration: {
    registrationId: 12, userId: 2, eventId: 7, occurrenceId: 2, date: appliedRows[1].date, status: "going",
    guestCount: 0, childcareCount: 0, childcareAgesNote: "", waitlistPosition: null, attendanceMode: "in_person",
  }, people: [] });

  renderPage();
  const forms = await screen.findAllByRole("form", { name: "Household answers" });
  const first = within(forms[0]);
  const sibling = within(forms[1]);
  await userEvent.click(first.getByRole("checkbox", { name: "Mary" }));
  await userEvent.selectOptions(first.getByLabelText("Answer for Mary"), "maybe");
  await chooseDatesForMe(forms[0]);
  await userEvent.click(await first.findByRole("button", { name: "Undo" }));
  await waitFor(() => expect(Events.get).toHaveBeenCalledTimes(3));
  expect(sibling.getByRole("checkbox", { name: "Me" })).not.toBeChecked();
  expect(first.getByRole("checkbox", { name: "Mary" })).toBeChecked();
  expect(first.getByLabelText("Answer for Mary")).toHaveValue("maybe");

  await userEvent.click(sibling.getByRole("checkbox", { name: "Me" }));
  await userEvent.click(sibling.getByRole("button", { name: "Save answers" }));
  expect(Events.register).toHaveBeenLastCalledWith(7, {
    date: appliedRows[1].date, people: [], status: "going", attendanceMode: "in_person", guestCount: 0,
    childcareCount: 0, childcareAgesNote: "",
  });
  await waitFor(() => expect(Events.get).toHaveBeenCalledTimes(4));
  await act(async () => held.resolve(appliedPage));
  expect(sibling.getByRole("checkbox", { name: "Me" })).toBeChecked();
  expect(within(sibling.getByRole("group", { name: "How you will attend" })).getByRole("radio", { name: "In person" })).toBeChecked();
});

it("clears protected event data while a terminal Undo refresh is still pending", async () => {
  const recurring = { ...page, event: { ...page.event, recurrenceRule: "FREQ=WEEKLY" }, occurrences: [occurrence({ recurrenceRule: "FREQ=WEEKLY" })] };
  const terminal = deferred<EventDetailResponse>();
  vi.mocked(Events.get)
    .mockResolvedValueOnce(recurring)
    .mockResolvedValueOnce(recurring)
    .mockReturnValueOnce(terminal.promise);
  vi.mocked(Events.registerSeries).mockResolvedValue({
    eventId: 7, scope: "all_future", status: "maybe",
    results: [{ occurrenceId: 1, date: "2026-12-01", before: {}, after: { registrationId: 4, status: "maybe" } }],
    refusals: [], undoToken: "undo-token", undoExpiresAt: new Date(Date.now() + 10_000).toISOString(),
  });
  vi.mocked(Events.undoSeriesRegistration).mockRejectedValue(new ApiError(409, "revoked", { code: "undo_authority_revoked" }));
  vi.mocked(Households.mine).mockResolvedValue({ household: null });
  renderPage();
  await answerAll(await screen.findByRole("group", { name: "Your answer" }), "Maybe");
  await userEvent.click(await screen.findByRole("button", { name: "Undo" }));
  await waitFor(() => expect(Events.get).toHaveBeenCalledTimes(3));
  expect(screen.queryByRole("heading", { name: "Hybrid study" })).not.toBeInTheDocument();
  await act(async () => terminal.reject(new ApiError(404, "hidden", { code: "not_found" })));
  expect(await screen.findByRole("heading", { name: "Event not found" })).toBeInTheDocument();
});

it("keeps the newest event refresh when an older quiet request resolves last", async () => {
  const recurring = { ...page, event: { ...page.event, recurrenceRule: "FREQ=WEEKLY" }, occurrences: [occurrence({ recurrenceRule: "FREQ=WEEKLY" })] };
  const old = deferred<EventDetailResponse>();
  const terminal = deferred<EventDetailResponse>();
  const stale = { ...recurring, event: { ...recurring.event, title: "Stale event details" } };
  const current = { ...recurring, event: { ...recurring.event, title: "Current event details" } };
  vi.mocked(Events.get)
    .mockResolvedValueOnce(recurring)
    .mockReturnValueOnce(old.promise)
    .mockReturnValueOnce(terminal.promise);
  vi.mocked(Events.registerSeries).mockResolvedValue({
    eventId: 7, scope: "all_future", status: "maybe",
    results: [{ occurrenceId: 1, date: "2026-12-01", before: {}, after: { registrationId: 4, status: "maybe" } }],
    refusals: [], undoToken: "undo-token", undoExpiresAt: new Date(Date.now() + 10_000).toISOString(),
  });
  vi.mocked(Events.undoSeriesRegistration).mockRejectedValue(new ApiError(409, "changed", { code: "undo_changed" }));
  vi.mocked(Households.mine).mockResolvedValue({ household: null });
  renderPage();
  await answerAll(await screen.findByRole("group", { name: "Your answer" }), "Maybe");
  await userEvent.click(await screen.findByRole("button", { name: "Undo" }));
  await waitFor(() => expect(Events.get).toHaveBeenCalledTimes(3));
  expect(screen.queryByRole("heading", { name: "Hybrid study" })).not.toBeInTheDocument();
  await act(async () => terminal.resolve(current));
  expect(await screen.findByRole("heading", { name: "Current event details" })).toBeInTheDocument();
  await act(async () => old.resolve(stale));
  expect(screen.getByRole("heading", { name: "Current event details" })).toBeInTheDocument();
  expect(screen.queryByRole("heading", { name: "Stale event details" })).not.toBeInTheDocument();
});

it("shows the public childcare ages and notes", async () => {
  vi.mocked(Events.get).mockResolvedValue({ ...page, event: { ...page.event, childcareAvailable: true, childcareAges: "2–5", childcareNotes: "Check in at the nursery." } });
  renderPage();
  expect(await screen.findByText("Childcare available · 2–5")).toBeInTheDocument();
  expect(screen.getByText("Check in at the nursery.")).toBeInTheDocument();
});

it("shares one household fetch across dates and refreshes server state after child-only answers", async () => {
  vi.mocked(Households.mine).mockResolvedValue({ household: { id: 1, name: "Family", address: "", createdBy: 2, accounts: [], people: [{ id: 3, householdId: 1, canConvert: false, firstName: "Mary", lastName: "", name: "Mary", kind: "adult", photoConsent: false, linkStatus: "none", registrationEligibility: "eligible", createdBy: 2 }] } });
  vi.mocked(Events.get).mockResolvedValue({ ...page, occurrences: [page.occurrences[0], { ...page.occurrences[0], id: 2, date: "2026-12-08" }] });
  vi.mocked(Events.register).mockResolvedValue({ registration: null, people: [{ registrationId: 12, personId: 3, registeredBy: 2, status: "going", attendanceMode: "in_person", waitlistPosition: null, childcareRequested: false }] });
  const before = vi.mocked(Events.get).mock.calls.length;
  const householdBefore = vi.mocked(Households.mine).mock.calls.length;
  renderPage(); const editors = await screen.findAllByRole("form", { name: "Household answers" });
  await userEvent.click(within(editors[0]).getByRole("checkbox", { name: "Mary" }));
  await userEvent.click(within(editors[0]).getByRole("button", { name: "Save answers" }));
  await waitFor(() => expect(Events.get).toHaveBeenCalledTimes(before + 2));
  expect(Households.mine).toHaveBeenCalledTimes(householdBefore + 1);
});

describe("EventDetail when line and date actions (DEC-92, DEC-93)", () => {
  // Noon UTC keeps the day and weekday the same in any real zone.
  const noon = { startsAt: "2026-12-01T12:00:00Z", endsAt: "2026-12-01T13:30:00Z", startsLocal: "2026-12-01T12:00", endsLocal: "2026-12-01T13:30", tzid: "UTC" };
  const series = { ...page.event, ...noon, recurrenceRule: "FREQ=WEEKLY;BYDAY=TU" };

  beforeEach(() => {
    vi.resetAllMocks();
    vi.mocked(Households.mine).mockResolvedValue({ household: null });
  });

  it("says when under the title, drops the list heading and slims the card for a single date", async () => {
    vi.mocked(Events.get).mockResolvedValue({ ...page, event: { ...page.event, ...noon }, occurrences: [occurrence({ ...noon, title: "Hybrid study", location: "Hall" })], canManage: false });
    renderPage();
    expect(await screen.findByText(/^Tuesday, Dec 1(, \d{4})? · \d/)).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Upcoming dates" })).not.toBeInTheDocument();
    // The card does not say the title or place again; only the header does.
    const card = screen.getByRole("article", { name: "Hybrid study" });
    expect(within(card).getByRole("heading", { name: "Hybrid study", level: 3 })).toHaveClass("sr-only");
    expect(within(card).queryByText("Hall")).not.toBeInTheDocument();
    expect(screen.getAllByText("Hall")).toHaveLength(1);
    expect(within(card).getByRole("group", { name: "Your answer" })).toBeInTheDocument();
    // One set of date actions, in the header, linking to the event page.
    expect(screen.getAllByRole("link", { name: /^Add to calendar/ })).toHaveLength(1);
    expect(screen.getByRole("link", { name: "Add to calendar" })).toHaveAttribute("href", "/api/v1/events/7/dates/2026-12-01/calendar.ics");
    expect(within(card).queryByRole("link", { name: /^Add to calendar/ })).not.toBeInTheDocument();
  });

  it("keeps a single date's leader More menu", async () => {
    vi.mocked(Events.get).mockResolvedValue({ ...page, event: { ...page.event, ...noon }, occurrences: [occurrence(noon)], canManage: true });
    renderPage();
    expect(await screen.findByRole("button", { name: "More for 2026-12-01" })).toBeInTheDocument();
  });

  it("shows a date's own title or place when it differs from the header", async () => {
    vi.mocked(Events.get).mockResolvedValue({ ...page, event: { ...page.event, ...noon }, occurrences: [occurrence({ ...noon, title: "Hybrid study, special", location: "Chapel" })] });
    renderPage();
    const card = await screen.findByRole("article", { name: "Hybrid study, special" });
    expect(within(card).getByText("Chapel")).toBeInTheDocument();
  });

  it("describes a series and keeps the list, with actions inside each card", async () => {
    const dates = [occurrence({ ...noon, recurrenceRule: series.recurrenceRule }), occurrence({ id: 2, date: "2026-12-08", startsAt: "2026-12-08T12:00:00Z", endsAt: "2026-12-08T13:30:00Z", recurrenceRule: series.recurrenceRule, tzid: "UTC" })];
    vi.mocked(Events.get).mockResolvedValue({ ...page, event: series, occurrences: dates, canManage: false });
    renderPage();
    expect(await screen.findByText(/^Every Tuesday · .+ · next Dec 1/)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Upcoming dates" })).toBeInTheDocument();
    const links = screen.getAllByRole("link", { name: /^Add to calendar/ });
    expect(links.map((l) => l.getAttribute("href"))).toEqual([
      "/api/v1/events/7/dates/2026-12-01/calendar.ics",
      "/api/v1/events/7/dates/2026-12-08/calendar.ics",
    ]);
    for (const link of links) expect(link.closest("article")).not.toBeNull();
  });

  it("keeps the list for an event with no recurrence rule but several dates", async () => {
    vi.mocked(Events.get).mockResolvedValue({ ...page, event: { ...page.event, ...noon }, occurrences: [occurrence(noon), occurrence({ id: 2, date: "2026-12-08", startsAt: "2026-12-08T12:00:00Z", endsAt: "2026-12-08T13:00:00Z", tzid: "UTC" })] });
    renderPage();
    expect(await screen.findByRole("heading", { name: "Upcoming dates" })).toBeInTheDocument();
  });

  it("calls a church-wide event church-wide", async () => {
    vi.mocked(Events.get).mockResolvedValue({ ...page, event: { ...page.event, ...noon, churchWide: true, hostedBy: "Fold" } });
    renderPage();
    expect(await screen.findByText("Church-wide event")).toBeInTheDocument();
    expect(screen.queryByText(/Hosted by/)).not.toBeInTheDocument();
  });
});

describe("EventDetail header actions and ?manage (DEC-99)", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    vi.mocked(Households.mine).mockResolvedValue({ household: null });
  });

  it("puts Edit series and More beside the title as secondary buttons for a manager", async () => {
    vi.mocked(Events.get).mockResolvedValue({ ...page, event: { ...page.event, recurrenceRule: "FREQ=WEEKLY" }, occurrences: [occurrence({ recurrenceRule: "FREQ=WEEKLY" })] });
    renderPage();
    const edit = await screen.findByRole("button", { name: "Edit series" });
    expect(edit).toHaveClass("btn-secondary");
    const title = screen.getByRole("heading", { level: 1, name: "Hybrid study" });
    expect(title.parentElement?.parentElement).toContainElement(edit);
    const more = screen.getByRole("button", { name: "More event actions" });
    expect(more).toHaveClass("btn-secondary");
    expect(title.parentElement?.parentElement).toContainElement(more);
    await userEvent.click(more);
    expect(screen.getByRole("menuitem", { name: "Delete event" })).toBeInTheDocument();
  });

  it("calls the button Edit event for a single date and toggles the editor", async () => {
    vi.mocked(Events.get).mockResolvedValue(page);
    renderPage();
    const edit = await screen.findByRole("button", { name: "Edit event" });
    expect(screen.queryByRole("link", { name: "Edit event" })).not.toBeInTheDocument();
    expect(edit).toHaveAttribute("aria-expanded", "false");
    await userEvent.click(edit);
    expect(screen.getByRole("button", { name: "Edit event" })).toHaveAttribute("aria-expanded", "true");
    expect(screen.queryByRole("button", { name: "Close editor" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Edit event" }));
    expect(screen.getByRole("button", { name: "Edit event" })).toHaveAttribute("aria-expanded", "false");
  });

  it("scrolls the editor into view and focuses its first field when it opens", async () => {
    vi.mocked(Events.get).mockResolvedValue(page);
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "Edit event" }));
    expect(scroll).toHaveBeenCalled();
    expect(screen.getByLabelText("Title")).toHaveFocus();
  });

  it("keeps Edit and More side by side in one row at every width", async () => {
    vi.mocked(Events.get).mockResolvedValue(page);
    renderPage();
    const edit = await screen.findByRole("button", { name: "Edit event" });
    const more = screen.getByRole("button", { name: "More event actions" });
    const row = edit.parentElement as HTMLElement;
    expect(row).toContainElement(more);
    expect(row).toHaveClass("flex");
    expect(row).not.toHaveClass("flex-col");
    expect(edit).toHaveClass("flex-1");
  });

  it("shows neither to someone who cannot manage the event", async () => {
    vi.mocked(Events.get).mockResolvedValue({ ...page, canManage: false });
    renderPage();
    await screen.findByRole("heading", { level: 1, name: "Hybrid study" });
    expect(screen.queryByRole("button", { name: /^Edit (series|event)$/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "More event actions" })).not.toBeInTheDocument();
  });

  it("opens Manage sign-ups for the ?manage date and scrolls it into view", async () => {
    vi.mocked(Events.get).mockResolvedValue(page);
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    renderPage("/events/7?manage=2026-12-01");
    expect(await screen.findByRole("region", { name: "Manage sign-ups" })).toBeInTheDocument();
    await waitFor(() => expect(scroll).toHaveBeenCalledTimes(1));
    expect(scroll.mock.contexts[0]).toBe(screen.getByRole("region", { name: "Manage sign-ups" }).parentElement);
  });

  it("ignores ?manage for someone who cannot manage, or for a date that is not listed", async () => {
    vi.mocked(Events.get).mockResolvedValue({ ...page, canManage: false });
    renderPage("/events/7?manage=2026-12-01");
    await screen.findByRole("heading", { level: 1, name: "Hybrid study" });
    expect(screen.queryByRole("region", { name: "Manage sign-ups" })).not.toBeInTheDocument();
    cleanup();
    vi.mocked(Events.get).mockResolvedValue(page);
    renderPage("/events/7?manage=2031-01-01");
    await screen.findByRole("button", { name: "More for 2026-12-01" });
    expect(screen.queryByRole("region", { name: "Manage sign-ups" })).not.toBeInTheDocument();
  });
});

describe("EventDetail shows a booked room as the place (M2.1b)", () => {
  const room = { id: 1, name: "Fellowship Hall", building: "Main" };
  beforeEach(() => {
    vi.resetAllMocks();
    vi.mocked(Households.mine).mockResolvedValue({ household: null });
  });

  it("puts the room in a single date's header, with the typed location after it", async () => {
    vi.mocked(Events.get).mockResolvedValue({ ...page, occurrences: [occurrence({ room })], canManage: false });
    renderPage();
    expect(await screen.findByText("Fellowship Hall · Main · Hall")).toBeInTheDocument();
  });

  it("puts the room on a series date's card and leaves the series header's typed location", async () => {
    vi.mocked(Events.get).mockResolvedValue({
      ...page, event: { ...page.event, recurrenceRule: "FREQ=WEEKLY;BYDAY=TU" },
      occurrences: [occurrence({ recurrenceRule: "FREQ=WEEKLY;BYDAY=TU", room }), occurrence({ id: 2, date: "2026-12-08", recurrenceRule: "FREQ=WEEKLY;BYDAY=TU" })],
      canManage: false,
    });
    renderPage();
    const cards = await screen.findAllByRole("article");
    expect(within(cards[0]).getByText(/ · Fellowship Hall · Main · Hall$/)).toBeInTheDocument();
    expect(within(cards[1]).getByText(/ · Hall$/)).not.toHaveTextContent("Fellowship Hall");
    // The header names the series' typed location only.
    expect(screen.getByText("Hall", { selector: "p" })).toBeInTheDocument();
  });

  it("shows the typed location exactly as before without a room", async () => {
    vi.mocked(Events.get).mockResolvedValue({ ...page, canManage: false });
    renderPage();
    expect(await screen.findByText("Hall")).toBeInTheDocument();
    expect(screen.queryByText(/Fellowship Hall/)).not.toBeInTheDocument();
  });
});

describe("EventDetail dates that need a room", () => {
  const series = {
    ...page,
    event: { ...page.event, recurrenceRule: "FREQ=WEEKLY;BYDAY=TU" },
    occurrences: [
      occurrence({ recurrenceRule: "FREQ=WEEKLY;BYDAY=TU" }),
      occurrence({ id: 2, date: "2026-12-08", startsLocal: "2026-12-08T19:00", recurrenceRule: "FREQ=WEEKLY;BYDAY=TU" }),
    ],
  };
  const needs = (...rows: NeedsRoom[]) =>
    vi.spyOn(SeriesRooms, "list").mockResolvedValue({ rooms: [], dates: 2, defaultRoom: null, needsRoom: rows, pending: 0 });

  beforeEach(() => {
    vi.resetAllMocks();
    vi.restoreAllMocks();
    vi.mocked(Households.mine).mockResolvedValue({ household: null });
    vi.mocked(Events.get).mockResolvedValue(series);
    vi.spyOn(Site, "get").mockResolvedValue({ name: "Fold", timezone: "UTC", selfSignup: "off", roomsEnabled: true });
    vi.spyOn(DateRooms, "list").mockResolvedValue({ rooms: [], booking: null });
  });

  it("lists them for a manager and opens that date's editor from Pick a room", async () => {
    needs({ date: "2026-12-08", roomName: "Fellowship Hall", status: "requested" });
    renderPage();
    expect(await screen.findByRole("heading", { name: /Dates that need a room/ })).toBeInTheDocument();
    expect(SeriesRooms.list).toHaveBeenCalledWith(7);
    expect(screen.getByText("Fellowship Hall isn't free then.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /^Pick a room for / }));
    // The second date's own editor, with its own Room control.
    await waitFor(() => expect(DateRooms.list).toHaveBeenCalledWith(7, "2026-12-08"));
    expect(DateRooms.list).not.toHaveBeenCalledWith(7, "2026-12-01");
    const room = await screen.findByRole("combobox", { name: /^Room/ });
    expect(room).toBeInTheDocument();
    // The cursor goes to the Room control, not the date field.
    await waitFor(() => expect(room).toHaveFocus());
  });

  it("shows nothing when no date needs a room", async () => {
    needs();
    renderPage();
    await waitFor(() => expect(SeriesRooms.list).toHaveBeenCalled());
    await screen.findAllByRole("article");
    expect(screen.queryByRole("heading", { name: /Dates that need a room/ })).not.toBeInTheDocument();
  });

  it("is not shown to people who can't manage the event, and asks for nothing", async () => {
    needs({ date: "2026-12-08", roomName: "Fellowship Hall", status: "requested" });
    vi.mocked(Events.get).mockResolvedValue({ ...series, canManage: false });
    renderPage();
    await screen.findAllByRole("article");
    expect(screen.queryByRole("heading", { name: /Dates that need a room/ })).not.toBeInTheDocument();
    expect(SeriesRooms.list).not.toHaveBeenCalled();
  });

  it("asks nothing and shows nothing while rooms are off", async () => {
    needs({ date: "2026-12-08", roomName: "Fellowship Hall", status: "requested" });
    vi.mocked(Site.get).mockResolvedValue({ name: "Fold", timezone: "UTC", selfSignup: "off", roomsEnabled: false });
    renderPage();
    await screen.findAllByRole("article");
    expect(screen.queryByRole("heading", { name: /Dates that need a room/ })).not.toBeInTheDocument();
    expect(SeriesRooms.list).not.toHaveBeenCalled();
  });
});

describe("Email people going (M3.4c)", () => {
  beforeEach(() => { vi.mocked(Households.mine).mockResolvedValue({ household: null }); });

  it("writes to the whole event from its menu, and to one date from that date's menu", async () => {
    composer.on = true;
    try {
      vi.mocked(Events.get).mockResolvedValue({ ...page, event: { ...page.event, recurrenceRule: "FREQ=WEEKLY" }, occurrences: [occurrence({ id: 41, recurrenceRule: "FREQ=WEEKLY" }), occurrence({ id: 42, date: "2026-12-08", recurrenceRule: "FREQ=WEEKLY" })] });
      const router = createMemoryRouter([{ path: "/events/:id", element: <EventDetail /> }, { path: "/messages/new", element: <p>Composer</p> }], { initialEntries: ["/events/7"] });
      render(<RouterProvider router={router} />);
      await userEvent.click(await screen.findByRole("button", { name: "More event actions" }));
      await userEvent.click(screen.getByRole("menuitem", { name: "Email people going" }));
      expect(router.state.location.search).toBe("?event=7");
      cleanup();
      const again = createMemoryRouter([{ path: "/events/:id", element: <EventDetail /> }, { path: "/messages/new", element: <p>Composer</p> }], { initialEntries: ["/events/7"] });
      render(<RouterProvider router={again} />);
      await userEvent.click(await screen.findByRole("button", { name: "More for 2026-12-08" }));
      await userEvent.click(screen.getByRole("menuitem", { name: "Email people going" }));
      expect(again.state.location.search).toBe("?occurrence=42");
    } finally {
      composer.on = false;
    }
  });

  it("isn't offered to someone who can't compose", async () => {
    vi.mocked(Events.get).mockResolvedValue(page);
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "More event actions" }));
    expect(screen.queryByRole("menuitem", { name: "Email people going" })).not.toBeInTheDocument();
  });
});
