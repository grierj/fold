import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { ApiError, Events, Site } from "../api";
import { Dates } from "../date-api";
import type { EventDate, RegistrationResponse } from "../types";
import { Messages } from "../messages-api";
import { EventDatePage } from "./EventDate";

const actor = vi.hoisted(() => ({ user: { id: 2 } as { id: number } | null, canCompose: false }));
vi.mock("../api", async (original) => ({ ...await original<typeof import("../api")>(), Events: { register: vi.fn() } }));
vi.mock("../auth", () => ({ useAuth: () => actor }));
vi.mock("../date-api", () => ({ Dates: { get: vi.fn(), register: vi.fn(), end: vi.fn() } }));
vi.mock("../messages-api", () => ({ Messages: { options: vi.fn() } }));
const occurrence = (overrides: Partial<EventDate> = {}): EventDate => ({ id: 51, eventId: 7, date: "2026-12-01", dateUrl: "/events/7/dates/2026-12-01", title: "One invited date", location: "Room 2", startsAt: "2026-12-01T19:00:00Z", endsAt: "2026-12-01T20:00:00Z", startsLocal: "2026-12-01T19:00", endsLocal: "2026-12-01T20:00", tzid: "UTC", durationMinutes: 60, remoteAvailable: true, guestsAllowed: 3, myRegistration: "going", myAttendanceMode: "remote", myGuestCount: 1, videoProvider: "link", videoUrl: "https://example.com/private-call", videoHidden: false, canSeeVideo: true, canSeeAttendees: false, canDecline: true, canBlock: true, ...overrides });
const registration: RegistrationResponse = { registrationId: 8, userId: 2, eventId: 7, occurrenceId: 51, date: "2026-12-01", status: "going", guestCount: 1, attendanceMode: "remote", childcareCount: 2, childcareAgesNote: "Preserve this", waitlistPosition: null };
function mount() { return render(<MemoryRouter initialEntries={["/events/7/dates/2026-12-01"]}><Routes><Route path="/events/:id/dates/:date" element={<EventDatePage />} /><Route path="/login" element={<p>Sign in first</p>} /></Routes></MemoryRouter>); }
beforeEach(() => { vi.resetAllMocks(); actor.user = { id: 2 }; actor.canCompose = false; vi.mocked(Dates.get).mockResolvedValue({ occurrence: occurrence() }); vi.mocked(Dates.register).mockResolvedValue({ registration }); vi.mocked(Dates.end).mockResolvedValue({}); vi.spyOn(window, "confirm").mockReturnValue(true); });

it("renders only date disclosures, never hidden parent/group/host/roster/household navigation", async () => {
  const row = { ...occurrence(), groupName: "SECRET GROUP", groupId: 88, hosts: [{ name: "SECRET HOST" }], attendees: [{ name: "SECRET ATTENDEE" }], description: "SECRET DESCRIPTION" };
  vi.mocked(Dates.get).mockResolvedValue({ occurrence: row });
  mount(); await screen.findByText("One invited date");
  expect(screen.getByText("You're going. See you there!")).toBeInTheDocument();
  expect(screen.queryByText(/Your answer:/)).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Going" })).toHaveAttribute("aria-pressed", "true");
  expect(screen.getByRole("button", { name: "Maybe" })).toHaveAttribute("aria-pressed", "false");
  expect(screen.queryByText(/SECRET/)).not.toBeInTheDocument();
  expect(screen.getAllByRole("link").map((a) => a.getAttribute("href"))).toEqual(["/calendar", "/api/v1/events/7/dates/2026-12-01/calendar.ics", "https://example.com/private-call"]);
  expect(screen.queryByRole("button", { name: /Manage|household|contact|all dates|Maybe for all|Can't go to any/i })).not.toBeInTheDocument();
});

it("shows an authorized ordinary group's intro under the single page heading", async () => {
  vi.mocked(Dates.get).mockResolvedValue({ occurrence: occurrence({ visibility: "private", groupId: 88, groupName: "Choir" }) });
  mount();
  expect(await screen.findByRole("heading", { level: 1, name: "One invited date" })).toBeInTheDocument();
  expect(screen.queryByRole("heading", { level: 2, name: "One invited date" })).not.toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Choir" })).toHaveAttribute("href", "/groups/88");
  expect(screen.getByRole("link", { name: "Back to calendar" })).toHaveAttribute("href", "/calendar");
  expect(screen.queryByRole("button", { name: "Refresh" })).not.toBeInTheDocument();
});

it("saves only own scoped answer/mode/guest fields, preserves unsupported care by omission and reflects server waitlisting", async () => {
  mount(); await screen.findByText("One invited date");
  expect(screen.getByLabelText("Remote")).toBeChecked(); expect(screen.getByLabelText("Guests")).toHaveValue(1);
  await userEvent.click(screen.getByLabelText("In person"));
  fireEvent.change(screen.getByLabelText("Guests"), { target: { value: "2" } });
  vi.mocked(Dates.get).mockResolvedValue({ occurrence: occurrence({ myRegistration: "waitlisted", myAttendanceMode: "in_person", myGuestCount: 2, canSeeVideo: false, videoUrl: undefined, videoHidden: true }) });
  await userEvent.click(screen.getByRole("button", { name: "Going" }));
  expect(Dates.register).toHaveBeenCalledWith("7", "2026-12-01", { status: "going", attendanceMode: "in_person", guestCount: 2 });
  await screen.findByText(/^You're on the waitlist/);
  expect(screen.getByRole("button", { name: "Going" })).toHaveAttribute("aria-pressed", "true");
  expect(screen.queryByRole("link", { name: /Join video call/ })).not.toBeInTheDocument();
  expect(screen.getByLabelText("Guests")).toHaveValue(2);
});

it("preserves valid values on capacity error, validates guests, and keeps Maybe explicit", async () => {
  vi.mocked(Dates.register).mockRejectedValue(new ApiError(409, "SECRET", { code: "event_full" }));
  mount(); await screen.findByText("One invited date");
  fireEvent.change(screen.getByLabelText("Guests"), { target: { value: "4" } });
  expect(screen.getByRole("button", { name: "Going" })).toBeDisabled();
  fireEvent.change(screen.getByLabelText("Guests"), { target: { value: "2" } });
  await userEvent.click(screen.getByRole("button", { name: "Maybe" }));
  expect(Dates.register).toHaveBeenCalledWith("7", "2026-12-01", { status: "maybe", attendanceMode: "remote", guestCount: 2 });
  expect(screen.getByRole("alert")).not.toHaveTextContent("SECRET");
  expect(screen.getByLabelText("Guests")).toHaveValue(2); expect(screen.getByLabelText("Remote")).toBeChecked();
});

it("keeps native keyboard activation and blocks disabled segment answers", async () => {
  mount(); await screen.findByText("One invited date");
  const maybe = screen.getByRole("button", { name: "Maybe" });
  maybe.focus();
  await userEvent.keyboard("{Enter}");
  expect(Dates.register).toHaveBeenCalledWith("7", "2026-12-01", { status: "maybe", attendanceMode: "remote", guestCount: 1 });
  await waitFor(() => expect(Dates.get).toHaveBeenCalledTimes(2));

  fireEvent.change(await screen.findByLabelText("Guests"), { target: { value: "4" } });
  const going = screen.getByRole("button", { name: "Going" });
  expect(going).toBeDisabled();
  await userEvent.click(going);
  expect(Dates.register).toHaveBeenCalledTimes(1);
});

it.each([
  ["going", "Going"], ["waitlisted", "Going"], ["maybe", "Maybe"], ["declined", "Can't go"], [null, null],
] as const)("maps %s to only the matching exact-date segment", async (status, selected) => {
  vi.mocked(Dates.get).mockResolvedValue({ occurrence: occurrence({ canRegister: true, canDecline: false, canBlock: false, myRegistration: status, myAttendanceMode: undefined, myGuestCount: undefined }) });
  mount(); await screen.findByText("One invited date");
  expect(screen.getByRole("status")).toHaveTextContent(status === "waitlisted" ? /on the waitlist/ : status === "going" ? "You're going. See you there!" : status === "maybe" ? "You said maybe" : status === "declined" ? "Got it. Only the leaders see this." : /^$/);
  for (const name of ["Going", "Maybe", "Can't go"]) {
    expect(screen.getByRole("button", { name })).toHaveAttribute("aria-pressed", String(name === selected));
  }
});

it("never invents unsupported options or edit authority for an ordinary/guardian read", async () => {
  vi.mocked(Dates.get).mockResolvedValue({ occurrence: occurrence({ canDecline: false, canBlock: false, canSeeVideo: false, videoHidden: false, myAttendanceMode: undefined, myGuestCount: undefined }) });
  mount(); await screen.findByText("One invited date");
  expect(screen.queryByRole("button", { name: /Going|Maybe|Can't go|Don't add/ })).not.toBeInTheDocument();
  expect(screen.queryByRole("link", { name: /Join video call/ })).not.toBeInTheDocument();
});

it("omits missing mode and guest values instead of manufacturing defaults", async () => {
  vi.mocked(Dates.get).mockResolvedValue({ occurrence: occurrence({ remoteAvailable: false, myAttendanceMode: undefined, myGuestCount: undefined }) });
  mount(); await userEvent.click(await screen.findByRole("button", { name: "Going" }));
  expect(Dates.register).toHaveBeenCalledWith("7", "2026-12-01", { status: "going" });
});

it.each([["Can't go", "decline"], ["Don't add me again", "block"]] as const)("confirms %s with safe scope copy, clears every disclosure, and never falls back", async (label, action) => {
  mount(); await screen.findByText("One invited date");
  vi.mocked(window.confirm).mockReturnValueOnce(false);
  await userEvent.click(screen.getByRole("button", { name: label })); expect(Dates.end).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: label }));
  expect(Dates.end).toHaveBeenCalledWith("7", "2026-12-01", action);
  expect(window.confirm).toHaveBeenLastCalledWith(expect.stringContaining("Other accepted dates stay yours"));
  await screen.findByText("This date is not available");
  expect(screen.queryByText("Room 2")).not.toBeInTheDocument(); expect(screen.queryByText("One invited date")).not.toBeInTheDocument();
  expect(screen.queryByRole("link", { name: /Join video call/ })).not.toBeInTheDocument();
  expect(Dates.get).toHaveBeenCalledTimes(1); expect(Dates.register).not.toHaveBeenCalled();
});

it.each([403, 404])("clears all prior date/video data on mutation %s without an ordinary RSVP retry", async (status) => {
  vi.mocked(Dates.register).mockRejectedValue(new ApiError(status, "SECRET"));
  mount(); await userEvent.click(await screen.findByRole("button", { name: "Going" }));
  await screen.findByText("This date is not available"); expect(screen.queryByText("One invited date")).not.toBeInTheDocument();
  expect(screen.queryByRole("link", { name: /Join video call/ })).not.toBeInTheDocument(); expect(Dates.register).toHaveBeenCalledTimes(1);
});

it.each([403, 404, 500])("fails closed on focus refresh %s and ignores a late mutation", async (status) => {
  let finish!: (value: { registration: RegistrationResponse }) => void;
  vi.mocked(Dates.register).mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
  mount(); await userEvent.click(await screen.findByRole("button", { name: "Going" }));
  vi.mocked(Dates.get).mockRejectedValue(new ApiError(status, "SECRET"));
  fireEvent(window, new Event("focus"));
  expect(screen.queryByText("One invited date")).not.toBeInTheDocument();
  await screen.findByText("This date is not available");
  await act(async () => finish({ registration }));
  expect(screen.queryByText("One invited date")).not.toBeInTheDocument(); expect(Dates.get).toHaveBeenCalledTimes(2);
});

it("ignores late reads after a newer focus refresh or unmount", async () => {
  let finish!: (value: { occurrence: EventDate }) => void;
  vi.mocked(Dates.get).mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; })).mockRejectedValue(new ApiError(404, "Ended"));
  const view = mount(); fireEvent(window, new Event("focus"));
  await screen.findByText("This date is not available");
  await act(async () => finish({ occurrence: occurrence() })); expect(screen.queryByText("One invited date")).not.toBeInTheDocument();
  view.unmount(); fireEvent(window, new Event("focus")); expect(Dates.get).toHaveBeenCalledTimes(2);
});

it("loads a public exact date signed out without invitation mutations", async () => {
  actor.user = null;
  vi.mocked(Dates.get).mockResolvedValue({ occurrence: occurrence({ visibility: "public", hostedBy: "Church", canDecline: false, canBlock: false, canSeeVideo: false, videoUrl: undefined, myRegistration: null }) });
  mount(); await screen.findByText("One invited date");
  expect(Dates.get).toHaveBeenCalledWith("7", "2026-12-01");
  expect(screen.getByText("Hosted by Church")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Register as a guest" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Don't add me again" })).not.toBeInTheDocument();
});

it("a confirmed end clears a newer focus read that completed before the end committed", async () => {
  let finish!: (value: Record<string, never>) => void;
  vi.mocked(Dates.end).mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
  mount(); await userEvent.click(await screen.findByRole("button", { name: "Can't go" }));
  fireEvent(window, new Event("focus")); await screen.findByText("One invited date");
  await act(async () => finish({})); await screen.findByText("This date is not available");
  expect(screen.queryByRole("link", { name: /Join video call/ })).not.toBeInTheDocument();
});

it("a late authority refusal clears a newer read without exposing server text", async () => {
  let fail!: (e: unknown) => void;
  vi.mocked(Dates.register).mockImplementation(() => new Promise((_resolve, reject) => { fail = reject; }));
  mount(); await userEvent.click(await screen.findByRole("button", { name: "Maybe" }));
  fireEvent(window, new Event("focus")); await screen.findByText("One invited date");
  await act(async () => fail(new ApiError(404, "SECRET"))); await screen.findByText("This date is not available");
  expect(screen.queryByText(/SECRET/)).not.toBeInTheDocument();
});

it("does not start another load or restore disclosures from a late mutation after unmount", async () => {
  let finish!: (value: { registration: RegistrationResponse }) => void;
  vi.mocked(Dates.register).mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
  const view = mount(); await userEvent.click(await screen.findByRole("button", { name: "Going" }));
  view.unmount(); await act(async () => finish({ registration })); expect(Dates.get).toHaveBeenCalledTimes(1);
});

it("lets an existing remote attendee switch in person when remote is no longer available", async () => {
  vi.mocked(Dates.get).mockResolvedValue({ occurrence: occurrence({ remoteAvailable: false }) });
  mount(); await screen.findByText("One invited date");
  expect(screen.getByLabelText(/Remote/)).toBeDisabled();
  await userEvent.click(screen.getByLabelText("In person")); await userEvent.click(screen.getByRole("button", { name: "Going" }));
  expect(Dates.register).toHaveBeenCalledWith("7", "2026-12-01", { status: "going", attendanceMode: "in_person", guestCount: 1 });
});

it("ignores an initial read that resolves after unmount", async () => {
  let finish!: (value: { occurrence: EventDate }) => void;
  vi.mocked(Dates.get).mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
  const view = mount(); view.unmount();
  await act(async () => finish({ occurrence: occurrence() }));
  expect(screen.queryByText("One invited date")).not.toBeInTheDocument(); expect(Dates.get).toHaveBeenCalledTimes(1);
});

 it("offers first ordinary RSVP options and withdraws without ending an invitation", async () => {
 vi.mocked(Dates.get).mockResolvedValue({ occurrence: occurrence({ canRegister: true, canDecline: false, canBlock: false, myRegistration: null, myAttendanceMode: undefined, myGuestCount: undefined }) });
 mount(); await screen.findByText("One invited date");
 expect(screen.getByLabelText("In person")).toBeChecked(); expect(screen.getByLabelText("Guests")).toHaveValue(0);
 await userEvent.click(screen.getByLabelText("Remote")); fireEvent.change(screen.getByLabelText("Guests"), {target:{value:"2"}});
 vi.mocked(Dates.get).mockResolvedValue({ occurrence: occurrence({canRegister:true,canDecline:false,canBlock:false,myAttendanceMode:"remote",myGuestCount:2}) });
 await userEvent.click(screen.getByRole("button",{name:"Going"})); await screen.findByText("You're going. See you there!");
 expect(Events.register).toHaveBeenCalledWith(7,{date:"2026-12-01",status:"going",attendanceMode:"remote",guestCount:2});
 vi.mocked(Dates.get).mockResolvedValue({ occurrence: occurrence({canRegister:true,canDecline:false,canBlock:false,myRegistration:"declined"}) });
 await userEvent.click(screen.getByRole("button",{name:"Can't go"})); await screen.findByText("Got it. Only the leaders see this.");
 expect(Events.register).toHaveBeenLastCalledWith(7,{date:"2026-12-01",status:"declined"}); expect(Dates.end).not.toHaveBeenCalled(); expect(Dates.register).not.toHaveBeenCalled(); expect(window.confirm).not.toHaveBeenCalled();
 });

it("shows a retained cancelled answer when the date is visible again", async () => {
 vi.mocked(Dates.get).mockResolvedValue({occurrence:occurrence({canRegister:true,canDecline:false,canBlock:false,myRegistration:"cancelled"})});
 mount(); expect(await screen.findByText("Your sign-up was cancelled.")).toBeInTheDocument();
});

it("lays the date out like the event page card: back link, when line, tile, place and date actions", async () => {
  vi.mocked(Dates.get).mockResolvedValue({ occurrence: occurrence({ startsAt: "2026-12-01T12:00:00Z", endsAt: "2026-12-01T13:30:00Z", startsLocal: "2026-12-01T12:00", endsLocal: "2026-12-01T13:30" }) });
  mount();
  const title = await screen.findByRole("heading", { level: 1, name: "One invited date" });
  const back = screen.getByRole("link", { name: "Back to calendar" });
  expect(back).toHaveTextContent("‹ Calendar");
  expect(back.compareDocumentPosition(title) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  const card = screen.getByRole("region", { name: "This event date" });
  expect(within(card).getByText(/^Tuesday, Dec 1(, \d{4})? · \d/)).toBeInTheDocument();
  expect(within(card).getByText("Dec")).toBeInTheDocument();
  expect(within(card).getByText("Room 2")).toBeInTheDocument();
  expect(within(card).getByRole("link", { name: "Add to calendar" })).toHaveAttribute("href", "/api/v1/events/7/dates/2026-12-01/calendar.ics");
  expect(within(card).getByRole("button", { name: "Share" })).toBeInTheDocument();
  // The date JSON has no counts, so none are shown.
  expect(within(card).queryByText(/seats|spots|\bgoing\b.*\d/i)).not.toBeInTheDocument();
});

it("says \"invited\" only for a real exact-date invitation", async () => {
  mount();
  expect(await screen.findByText("You're invited to this date.")).toBeInTheDocument();
  cleanup();
  vi.mocked(Dates.get).mockResolvedValue({ occurrence: occurrence({ canRegister: true, canDecline: false, canBlock: false, visibility: "public", hostedBy: "Church" }) });
  mount(); await screen.findByText("One invited date");
  expect(screen.queryByText(/invited to this date/)).not.toBeInTheDocument();
});

it("shows the waitlist hint only before Going is saved", async () => {
  vi.mocked(Dates.get).mockResolvedValue({ occurrence: occurrence({ myRegistration: "maybe" }) });
  mount();
  expect(await screen.findByText("If it’s full, Going puts you on the waitlist.")).toBeInTheDocument();
  cleanup();
  vi.mocked(Dates.get).mockResolvedValue({ occurrence: occurrence({ myRegistration: "going" }) });
  mount(); await screen.findByText("One invited date");
  expect(screen.queryByText(/Going puts you on the waitlist/)).not.toBeInTheDocument();
});

it("shows the booked room as the place, with the typed location after it (M2.1b)", async () => {
  vi.mocked(Dates.get).mockResolvedValue({ occurrence: occurrence({ room: { id: 1, name: "Fellowship Hall", building: "Main" } }) });
  mount();
  expect(await screen.findByText("Fellowship Hall · Main · Room 2")).toBeInTheDocument();
});

it("shows only the typed location when no room is booked (M2.1b)", async () => {
  mount();
  expect(await screen.findByText("Room 2")).toBeInTheDocument();
});

it("offers Email people going for this date to someone who may write to the event (M3.4c)", async () => {
  actor.canCompose = true;
  vi.mocked(Messages.options).mockResolvedValue({ canCompose: true, canMessageEveryone: false, smsEnabled: false, groups: [], events: [{ id: 7, title: "One invited date", churchWide: true, dates: [] }] });
  mount();
  expect(await screen.findByRole("link", { name: "Email people going" })).toHaveAttribute("href", "/messages/new?occurrence=51");
  expect(Messages.options).toHaveBeenCalledWith(7);
});

it("doesn't offer it for an event the viewer can't write to, or ask when they can't compose", async () => {
  actor.canCompose = true;
  vi.mocked(Messages.options).mockResolvedValue({ canCompose: true, canMessageEveryone: false, smsEnabled: false, groups: [], events: [{ id: 8, title: "Other", churchWide: true, dates: [] }] });
  mount(); await screen.findByText("One invited date");
  await waitFor(() => expect(Messages.options).toHaveBeenCalled());
  expect(screen.queryByRole("link", { name: "Email people going" })).not.toBeInTheDocument();
  cleanup(); vi.mocked(Messages.options).mockClear(); actor.canCompose = false;
  mount(); await screen.findByText("One invited date");
  expect(Messages.options).not.toHaveBeenCalled();
});

it("shows its event's managers the date's reminder lines, each sent one linking to its history (M3.6b)", async () => {
  vi.spyOn(Site, "get").mockResolvedValue({ name: "Grace", timezone: "America/Boise", selfSignup: "off", roomsEnabled: false });
  vi.mocked(Dates.get).mockResolvedValue({ occurrence: occurrence({ canDecline: false, canBlock: false, reminders: {
    minutes: 1440, dueAt: "2026-01-30T19:00:00Z",
    messages: [
      { messageId: 61, kind: "going", status: "sent", sentAt: "2026-01-30T19:00:00Z", reached: 12, failed: 0 },
      { messageId: 62, kind: "waitlist", status: "sent", sentAt: "2026-01-30T19:00:00Z", reached: 3, failed: 0 },
    ],
  } }) });
  mount();
  expect(await screen.findByRole("heading", { level: 2, name: "Reminder email" })).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Reminder emailed to 12 people · Fri, Jan 30 at 12:00 PM" })).toHaveAttribute("href", "/messages/61");
  expect(screen.getByRole("link", { name: "Waitlist note emailed to 3 people · Fri, Jan 30 at 12:00 PM" })).toHaveAttribute("href", "/messages/62");
});

it("says before it goes when the reminder will, and when reminders are off, without a link", async () => {
  vi.spyOn(Site, "get").mockResolvedValue({ name: "Grace", timezone: "America/Boise", selfSignup: "off", roomsEnabled: false });
  vi.mocked(Dates.get).mockResolvedValue({ occurrence: occurrence({ startsAt: "2030-12-03T02:00:00Z", reminders: { minutes: 2880, dueAt: "2030-12-01T02:00:00Z", messages: [] } }) });
  mount();
  expect(await screen.findByText("Reminder goes out Sat, Nov 30 at 7:00 PM")).toBeInTheDocument();
  cleanup();
  vi.mocked(Dates.get).mockResolvedValue({ occurrence: occurrence({ reminders: { minutes: 0, dueAt: null, messages: [] } }) });
  mount();
  expect(await screen.findByText("Reminders are off for this event")).toBeInTheDocument();
  expect(screen.queryByRole("link", { name: /Reminder/ })).not.toBeInTheDocument();
});

it("shows no reminder lines to anyone the API doesn't send them to", async () => {
  mount(); await screen.findByText("One invited date");
  expect(screen.queryByText("Reminder email")).not.toBeInTheDocument();
});
