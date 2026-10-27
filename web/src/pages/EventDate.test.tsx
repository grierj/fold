import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { ApiError, Events } from "../api";
import { Dates } from "../date-api";
import type { EventDate, RegistrationResponse } from "../types";
import { EventDatePage } from "./EventDate";

const actor = vi.hoisted(() => ({ user: { id: 2 } as { id: number } | null }));
vi.mock("../api", async (original) => ({ ...await original<typeof import("../api")>(), Events: { register: vi.fn() } }));
vi.mock("../auth", () => ({ useAuth: () => actor }));
vi.mock("../date-api", () => ({ Dates: { get: vi.fn(), register: vi.fn(), end: vi.fn() } }));
const occurrence = (overrides: Partial<EventDate> = {}): EventDate => ({ id: 51, eventId: 7, date: "2026-12-01", dateUrl: "/events/7/dates/2026-12-01", title: "One invited date", location: "Room 2", startsAt: "2026-12-01T19:00:00Z", endsAt: "2026-12-01T20:00:00Z", startsLocal: "2026-12-01T19:00", endsLocal: "2026-12-01T20:00", tzid: "UTC", durationMinutes: 60, remoteAvailable: true, guestsAllowed: 3, myRegistration: "going", myAttendanceMode: "remote", myGuestCount: 1, videoProvider: "link", videoUrl: "https://example.com/private-call", videoHidden: false, canSeeVideo: true, canSeeAttendees: false, canDecline: true, canBlock: true, ...overrides });
const registration: RegistrationResponse = { registrationId: 8, userId: 2, eventId: 7, occurrenceId: 51, date: "2026-12-01", status: "going", guestCount: 1, attendanceMode: "remote", childcareCount: 2, childcareAgesNote: "Preserve this", waitlistPosition: null };
function mount() { return render(<MemoryRouter initialEntries={["/events/7/dates/2026-12-01"]}><Routes><Route path="/events/:id/dates/:date" element={<EventDatePage />} /><Route path="/login" element={<p>Sign in first</p>} /></Routes></MemoryRouter>); }
beforeEach(() => { vi.resetAllMocks(); actor.user = { id: 2 }; vi.mocked(Dates.get).mockResolvedValue({ occurrence: occurrence() }); vi.mocked(Dates.register).mockResolvedValue({ registration }); vi.mocked(Dates.end).mockResolvedValue({}); vi.spyOn(window, "confirm").mockReturnValue(true); });

it("renders only date disclosures, never hidden parent/group/host/roster/household navigation", async () => {
  const row = { ...occurrence(), groupName: "SECRET GROUP", groupId: 88, hosts: [{ name: "SECRET HOST" }], attendees: [{ name: "SECRET ATTENDEE" }], description: "SECRET DESCRIPTION" };
  vi.mocked(Dates.get).mockResolvedValue({ occurrence: row });
  mount(); await screen.findByText("One invited date");
  expect(screen.getByText("Your answer: Going")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Going" })).toHaveAttribute("aria-pressed", "true");
  expect(screen.getByRole("button", { name: "Maybe" })).toHaveAttribute("aria-pressed", "false");
  expect(screen.queryByText(/SECRET/)).not.toBeInTheDocument();
  expect(screen.getAllByRole("link").map((a) => a.getAttribute("href"))).toEqual(["/calendar", "https://example.com/private-call"]);
  expect(screen.queryByRole("button", { name: /Manage|household|contact|all dates|Maybe for all|Can't go to any/i })).not.toBeInTheDocument();
});

it("saves only own scoped answer/mode/guest fields, preserves unsupported care by omission and reflects server waitlisting", async () => {
  mount(); await screen.findByText("One invited date");
  expect(screen.getByLabelText("Remote")).toBeChecked(); expect(screen.getByLabelText("Guests")).toHaveValue(1);
  await userEvent.click(screen.getByLabelText("In person"));
  fireEvent.change(screen.getByLabelText("Guests"), { target: { value: "2" } });
  vi.mocked(Dates.get).mockResolvedValue({ occurrence: occurrence({ myRegistration: "waitlisted", myAttendanceMode: "in_person", myGuestCount: 2, canSeeVideo: false, videoUrl: undefined, videoHidden: true }) });
  await userEvent.click(screen.getByRole("button", { name: "Going" }));
  expect(Dates.register).toHaveBeenCalledWith("7", "2026-12-01", { status: "going", attendanceMode: "in_person", guestCount: 2 });
  await screen.findByText("Your answer: Waitlisted");
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
 await userEvent.click(screen.getByRole("button",{name:"Going"})); await screen.findByText("Your answer: Going");
 expect(Events.register).toHaveBeenCalledWith(7,{date:"2026-12-01",status:"going",attendanceMode:"remote",guestCount:2});
 vi.mocked(Dates.get).mockResolvedValue({ occurrence: occurrence({canRegister:true,canDecline:false,canBlock:false,myRegistration:"declined"}) });
 await userEvent.click(screen.getByRole("button",{name:"Can't go"})); await screen.findByText("Your answer: Can't go");
 expect(Events.register).toHaveBeenLastCalledWith(7,{date:"2026-12-01",status:"declined"}); expect(Dates.end).not.toHaveBeenCalled(); expect(Dates.register).not.toHaveBeenCalled(); expect(window.confirm).not.toHaveBeenCalled();
 });

it("shows a retained cancelled answer when the date is visible again", async () => {
 vi.mocked(Dates.get).mockResolvedValue({occurrence:occurrence({canRegister:true,canDecline:false,canBlock:false,myRegistration:"cancelled"})});
 mount(); expect(await screen.findByText("Your answer: Cancelled")).toBeInTheDocument();
});
