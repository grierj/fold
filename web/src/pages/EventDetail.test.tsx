import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, Events, Households } from "../api";
import type { EventDetailResponse } from "../types";
import { occurrence } from "../test/occurrence";
import { EventDetail } from "./EventDetail";

vi.mock("../auth", () => ({ useAuth: () => ({ user: { id: 2 }, loading: false, isAdmin: false }) }));
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
    capacity: null, waitlistEnabled: false, guestsAllowed: 0,
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

function renderPage() {
  render(<MemoryRouter initialEntries={["/events/7"]}><Routes><Route path="/events/:id" element={<EventDetail />} /></Routes></MemoryRouter>);
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((accept, decline) => { resolve = accept; reject = decline; });
  return { promise, resolve, reject };
}

async function editDate() {
  renderPage();
  await userEvent.click(await screen.findByRole("button", { name: "More for 2026-12-01" }));
  await userEvent.click(screen.getByRole("menuitem", { name: "Edit this date" }));
  return screen.getByRole("textbox", { name: /^Remote seat limit for this date/ });
}

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
  await userEvent.click((await screen.findAllByRole("button", { name: "Maybe for all" }))[0]);
  expect(await screen.findByRole("button", { name: "Undo" })).toBeInTheDocument();
  const answerGroups = screen.getAllByRole("group", { name: "Your answer" });
  await userEvent.click(within(answerGroups[1]).getByRole("button", { name: "Going" }));
  await userEvent.click(screen.getByRole("button", { name: "Just this date" }));
  await waitFor(() => expect(screen.queryByRole("button", { name: "Undo" })).not.toBeInTheDocument());
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
  renderPage();
  await userEvent.click(await screen.findByRole("button", { name: "Maybe for all" }));
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
  renderPage();
  await userEvent.click(await screen.findByRole("button", { name: "Maybe for all" }));
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
