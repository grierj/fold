import { MemoryRouter } from "react-router-dom";
import { useState } from "react";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { RsvpButtons } from "./RsvpButtons";
import { ApiError, Events } from "../api";
import { occurrence } from "../test/occurrence";

vi.mock("../api", async (importActual) => ({
  ApiError: (await importActual<typeof import("../api")>()).ApiError,
  Events: { register: vi.fn(), registerSeries: vi.fn(), undoSeriesRegistration: vi.fn() },
}));


describe("RsvpButtons", () => {
  beforeEach(() => vi.resetAllMocks());
  it("shows the full state with no waitlist and disables Going", () => {
    render(
      <RsvpButtons
        instance={occurrence({ capacity: 10, seatsUsed: 10, spotsLeft: 0, waitlistEnabled: false })}
        onChange={() => {}}
      />
    );
    expect(screen.getByText("This event is full.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Going" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Maybe" })).not.toBeDisabled();
  });

  it.each([
    ["going", "Going"], ["maybe", "Maybe"], ["declined", "Can't go"], ["waitlisted", "Going"], [null, null],
  ] as const)("maps %s to the selected RSVP answer", (status, selected) => {
    render(<RsvpButtons instance={occurrence({ myRegistration: status })} onChange={() => {}} />);
    for (const name of ["Going", "Maybe", "Can't go"]) {
      expect(screen.getByRole("button", { name })).toHaveAttribute("aria-pressed", String(name === selected));
    }
    if (status === "waitlisted") expect(screen.getByText(/Waitlisted/)).toBeInTheDocument();
  });

  it("shows the waitlisted state with the position", () => {
    render(
      <RsvpButtons
        instance={occurrence({
          capacity: 10,
          seatsUsed: 10,
          spotsLeft: 0,
          waitlistEnabled: true,
          myRegistration: "waitlisted",
          myWaitlistPosition: 3,
        })}
        onChange={() => {}}
      />
    );
    expect(screen.getByText(/position 3/)).toBeInTheDocument();
  });

  it("shows the removed state with no buttons", () => {
    render(
      <RsvpButtons instance={occurrence({ myRegistration: "removed" })} onChange={() => {}} />
    );
    expect(screen.getByText(/removed your registration/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Going" })).not.toBeInTheDocument();
  });

  it("hides the attendance choice when remote is not available", () => {
    render(<RsvpButtons instance={occurrence({})} onChange={() => {}} />);
    expect(screen.queryByText("How you'll attend")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remote" })).not.toBeInTheDocument();
  });

  it("shows In person / Remote and registers in the picked pool", async () => {
    vi.mocked(Events.register).mockResolvedValue({ people: [],
      registration: {
        registrationId: 31, userId: 3, eventId: 7, occurrenceId: 1, date: "2026-12-01",
        status: "going", guestCount: 0, childcareCount: 0, childcareAgesNote: "", waitlistPosition: null, attendanceMode: "remote",
      },
    });
    const onChange = vi.fn();
    render(
      <RsvpButtons
        instance={occurrence({ remoteAvailable: true, remoteCapacity: 5, remoteSeatsUsed: 1, remoteSpotsLeft: 4 })}
        onChange={onChange}
      />
    );
    expect(screen.getByText("How you'll attend")).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "In person" })).toBeChecked();
    await userEvent.click(screen.getByRole("radio", { name: "Remote" }));
    expect(Events.register).not.toHaveBeenCalled();
    expect(screen.getByText("Tap Going to attend remotely.").parentElement).toBeInTheDocument();
    expect(screen.getByText("Tap Going to attend remotely.")).toHaveAttribute("aria-live", "polite");
    await userEvent.click(screen.getByRole("button", { name: "Going" }));
    expect(Events.register).toHaveBeenCalledWith(7, { status: "going", date: "2026-12-01", guestCount: 0, attendanceMode: "remote" });
    expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ registration: expect.objectContaining({ attendanceMode: "remote" }) }));
  });

  it("starts on my saved mode and offers the switch back", () => {
    render(
      <RsvpButtons
        instance={occurrence({
          remoteAvailable: true,
          myRegistration: "going",
          myAttendanceMode: "remote",
        })}
        onChange={() => {}}
      />
    );
    expect(screen.getByRole("radio", { name: "Remote" })).toBeChecked();
  });

  it("disables Going when the picked remote pool is full with no waitlist", async () => {
    render(
      <RsvpButtons
        instance={occurrence({
          capacity: 10, seatsUsed: 0, spotsLeft: 10,
          remoteAvailable: true, remoteCapacity: 2, remoteSeatsUsed: 2, remoteSpotsLeft: 0,
          waitlistEnabled: false,
        })}
        onChange={() => {}}
      />
    );
    await userEvent.click(screen.getByRole("radio", { name: "Remote" }));
    expect(screen.getByText("Remote seats are full. You can still come in person.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Going" })).toBeDisabled();
  });

  it("uses the shared series scope dialog, confirms bulk Can't go, and offers Undo for ten seconds", async () => {
    vi.mocked(Events.registerSeries).mockResolvedValue({ eventId: 7, scope: "this_and_following", date: "2026-12-01", status: "going", results: [{ occurrenceId: 1, date: "2026-12-01", before: {}, after: { registrationId: 4, status: "waitlisted", waitlistPosition: 2 } }], refusals: [], undoToken: "undo-token", undoExpiresAt: new Date(Date.now() + 10_000).toISOString() });
    vi.mocked(Events.undoSeriesRegistration).mockResolvedValue({ eventId: 7, scope: "", status: "", results: [{ occurrenceId: 1, date: "2026-12-01", before: { status: "waitlisted" }, after: { status: "going" } }], refusals: [], capacityLoss: [] });
    const onChange = vi.fn();
    render(<RsvpButtons instance={occurrence({ recurrenceRule: "FREQ=WEEKLY" })} onChange={onChange} />);
    await userEvent.click(screen.getByRole("button", { name: "Going" }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Just this date" })).toHaveFocus();
    await userEvent.click(screen.getByRole("button", { name: "This and following dates" }));
    expect(Events.registerSeries).toHaveBeenCalledWith(7, expect.objectContaining({ date: "2026-12-01", scope: "this_and_following", status: "going" }));
    expect(await screen.findByRole("status")).toHaveTextContent("waitlisted for 2026-12-01");
    await userEvent.click(screen.getByRole("button", { name: "Undo" }));
    expect(Events.undoSeriesRegistration).toHaveBeenCalledWith(7, "undo-token");
    expect(screen.getByRole("status")).toHaveTextContent("Undo: Going to 1 date");

    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    await userEvent.click(screen.getByRole("button", { name: "Can't go to any" }));
    expect(confirm).toHaveBeenCalled();
    expect(Events.registerSeries).toHaveBeenCalledTimes(1);
  });

  it("single events bypass the scope dialog", async () => {
    vi.mocked(Events.register).mockResolvedValue({ people: [], registration: { registrationId: 31, userId: 3, eventId: 7, occurrenceId: 1, date: "2026-12-01", status: "maybe", guestCount: 0, childcareCount: 0, childcareAgesNote: "", waitlistPosition: null, attendanceMode: "in_person" } });
    render(<RsvpButtons instance={occurrence({ recurrenceRule: "" })} onChange={() => {}} />);
    await userEvent.click(screen.getByRole("button", { name: "Maybe" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(Events.register).toHaveBeenCalledOnce();
  });

  it("shows the guest count input when guests are allowed", () => {
    render(
      <RsvpButtons instance={occurrence({ guestsAllowed: 2, myRegistration: "going" })} onChange={() => {}} />
    );
    expect(screen.getByLabelText(/Extra guests/)).toBeInTheDocument();
    expect(screen.getByText(/Up to 2/)).toBeInTheDocument();
  });
});


describe("remote attendance changes", () => {
  beforeEach(() => vi.resetAllMocks());

  it.each([
    ["in_person", "remote", "Remote", "Switched to remote"],
    ["remote", "in_person", "In person", "Switched to in person"],
  ] as const)("saves a going person's switch from %s to %s immediately", async (before, after, label, message) => {
    const registration = { registrationId: 31, userId: 3, eventId: 7, occurrenceId: 1, date: "2026-12-01", status: "going" as const, guestCount: 1, childcareCount: 0, childcareAgesNote: "", waitlistPosition: null, attendanceMode: after };
    vi.mocked(Events.register).mockResolvedValue({ people: [], registration });
    const onChange = vi.fn();
    render(<RsvpButtons instance={occurrence({ remoteAvailable: true, myRegistration: "going", myAttendanceMode: before, myGuestCount: 1 })} onChange={onChange} />);
    expect(screen.getByRole("group", { name: "How you'll attend" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("radio", { name: label }));
    expect(Events.register).toHaveBeenCalledExactlyOnceWith(7, { status: "going", date: "2026-12-01", guestCount: 1, attendanceMode: after });
    expect(onChange).toHaveBeenCalledWith({ registration, people: [] });
    expect(screen.getByRole("status")).toHaveTextContent(message);
  });

  it("restores the saved choice if switching fails", async () => {
    vi.mocked(Events.register).mockRejectedValue(new Error("offline"));
    render(<RsvpButtons instance={occurrence({ remoteAvailable: true, myRegistration: "going", myAttendanceMode: "in_person" })} onChange={() => {}} />);
    await userEvent.click(screen.getByRole("radio", { name: "Remote" }));
    await waitFor(() => expect(screen.getByRole("radio", { name: "In person" })).toBeChecked());
    expect(screen.getByRole("alert")).toHaveTextContent("We couldn't reach Fold. Check your connection and try again.");
    expect(screen.getByRole("status")).toBeEmptyDOMElement();
  });

  it("names the remote pool if a switch is refused because it filled up", async () => {
    vi.mocked(Events.register).mockRejectedValue(new ApiError(409, "This event is full", { code: "event_full" }));
    render(<RsvpButtons instance={occurrence({ remoteAvailable: true, myRegistration: "going", myAttendanceMode: "in_person" })} onChange={() => {}} />);
    await userEvent.click(screen.getByRole("radio", { name: "Remote" }));
    expect(screen.getByRole("alert")).toHaveTextContent("Remote seats are full. You can still come in person.");
    expect(screen.getByRole("radio", { name: "In person" })).toBeChecked();
  });

  it("announces waitlisting instead of claiming a seat after a switch", async () => {
    vi.mocked(Events.register).mockResolvedValue({ people: [], registration: { registrationId: 31, userId: 3, eventId: 7, occurrenceId: 1, date: "2026-12-01", status: "waitlisted", guestCount: 0, childcareCount: 0, childcareAgesNote: "", waitlistPosition: 2, attendanceMode: "remote" } });
    render(<RsvpButtons instance={occurrence({ remoteAvailable: true, myRegistration: "going", waitlistEnabled: true })} onChange={() => {}} />);
    await userEvent.click(screen.getByRole("radio", { name: "Remote" }));
    expect(screen.getByRole("status")).toHaveTextContent("You're on the remote waitlist.");
  });

  it("names the remote waitlist when remote seats are full", async () => {
    render(<RsvpButtons instance={occurrence({ remoteAvailable: true, remoteCapacity: 2, remoteSpotsLeft: 0, waitlistEnabled: true })} onChange={() => {}} />);
    await userEvent.click(screen.getByRole("radio", { name: "Remote" }));
    expect(screen.getByText("Remote seats are full. You can still come in person, or join the remote waitlist.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Going" })).toBeEnabled();
  });

  it("hides Remote when this date has zero remote seats and registers in person", async () => {
    vi.mocked(Events.register).mockResolvedValue({ people: [], registration: { registrationId: 31, userId: 3, eventId: 7, occurrenceId: 1, date: "2026-12-01", status: "going", guestCount: 0, childcareCount: 0, childcareAgesNote: "", waitlistPosition: null, attendanceMode: "in_person" } });
    render(<RsvpButtons instance={occurrence({ remoteAvailable: true, remoteCapacity: 0, remoteSpotsLeft: 0, myAttendanceMode: "remote" })} onChange={() => {}} />);
    expect(screen.queryByRole("radio", { name: "Remote" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Going" }));
    expect(Events.register).toHaveBeenCalledExactlyOnceWith(7, { status: "going", date: "2026-12-01", guestCount: 0, attendanceMode: "in_person" });
  });
});

describe("childcare RSVP", () => {
  beforeEach(() => vi.resetAllMocks());
  const registration = {
    registrationId: 31, userId: 3, eventId: 7, occurrenceId: 1, date: "2026-12-01", status: "going", guestCount: 0,
    waitlistPosition: null, attendanceMode: "in_person", childcareCount: 0, childcareAgesNote: "",
  } satisfies import("../types").RegistrationResponse;

  function SavedRsvp({ initial = occurrence({ childcareAvailable: true }) }) {
    const [instance, setInstance] = useState(initial);
    return <RsvpButtons instance={instance} onChange={({ registration: reg }) => reg && setInstance((old) => ({
      ...old, myRegistration: reg.status, myChildcareCount: reg.childcareCount,
      myChildcareAgesNote: reg.childcareAgesNote, myAttendanceMode: reg.attendanceMode,
    }))} />;
  }

  it("saves Going with zero children before showing it as selected", async () => {
    let complete = () => {};
    vi.mocked(Events.register).mockImplementation(() => new Promise((resolve) => {
      complete = () => resolve({ people: [], registration });
    }));
    render(<SavedRsvp />);
    expect(screen.queryByLabelText(/^Children needing childcare/)).not.toBeInTheDocument();
    const going = screen.getByRole("button", { name: "Going" });
    await userEvent.click(going);
    expect(Events.register).toHaveBeenCalledExactlyOnceWith(7, { status: "going", date: "2026-12-01", guestCount: 0, attendanceMode: undefined, ...{ childcareCount: 0, childcareAgesNote: "" } });
    expect(going).toHaveAttribute("aria-pressed", "false");
    expect(screen.queryByText("Childcare for this date")).not.toBeInTheDocument();
    await act(async () => complete());
    expect(going).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByText("Childcare for this date")).toBeInTheDocument();
    expect(screen.getByLabelText(/^Children needing childcare/)).toHaveValue("0");
    expect(screen.getByRole("status")).toHaveTextContent("Saved: going, no childcare");
    expect(screen.getByRole("status")).toHaveFocus();
  });

  it("leaves Going unselected when the first save fails", async () => {
    vi.mocked(Events.register).mockRejectedValue(new Error("offline"));
    render(<SavedRsvp />);
    await userEvent.click(screen.getByRole("button", { name: "Going" }));
    expect(screen.getByRole("button", { name: "Going" })).toHaveAttribute("aria-pressed", "false");
    expect(screen.queryByText("Childcare for this date")).not.toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("We couldn't reach Fold.");
  });

  it.each([1, 2, 0])("saves %s children separately and announces the saved request", async (count) => {
    vi.mocked(Events.register).mockResolvedValue({ people: [], registration: { ...registration, childcareCount: count, childcareAgesNote: "3 and 7" } });
    render(<SavedRsvp initial={occurrence({ childcareAvailable: true, myRegistration: "going" })} />);
    const input = screen.getByLabelText(/^Children needing childcare/);
    await userEvent.clear(input);
    await userEvent.type(input, String(count));
    await userEvent.type(screen.getByLabelText(/^Ages/), " 3 and 7 ");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(Events.register).toHaveBeenCalledExactlyOnceWith(7, { status: "going", date: "2026-12-01", guestCount: 0, attendanceMode: undefined, ...{ childcareCount: count, childcareAgesNote: "3 and 7" } });
    expect(screen.getByRole("status")).toHaveTextContent(count === 0 ? "Saved: going, no childcare"
      : count === 1 ? "Saved: going, 1 child in childcare" : "Saved: going, 2 children in childcare");
    expect(screen.getByRole("status")).toHaveFocus();
    expect(screen.getByRole("button", { name: "Save" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Going" })).toHaveAttribute("aria-pressed", "true");
  });

  it("ties a full response to the count and offers Clear childcare request", async () => {
    vi.mocked(Events.register)
      .mockRejectedValueOnce(new ApiError(409, "childcare is full for this date", { code: "childcare_full" }))
      .mockResolvedValueOnce({ people: [], registration });
    render(<SavedRsvp initial={occurrence({ childcareAvailable: true, myRegistration: "going" })} />);
    const count = screen.getByLabelText(/^Children needing childcare/);
    await userEvent.clear(count);
    await userEvent.type(count, "2");
    await userEvent.type(screen.getByLabelText(/^Ages/), "3 and 7");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(count).toHaveValue("2");
    expect(count).toHaveAttribute("aria-invalid", "true");
    expect(count).toHaveAccessibleDescription(expect.stringContaining("Childcare is full for this date. Your answers haven't changed."));
    expect(count).toHaveFocus();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByLabelText(/^Ages/)).toHaveValue("3 and 7");
    expect(screen.getByRole("button", { name: "Going" })).toHaveAttribute("aria-pressed", "true");
    await userEvent.click(screen.getByRole("button", { name: "Clear childcare request" }));
    expect(Events.register).toHaveBeenLastCalledWith(7, { status: "going", date: "2026-12-01", guestCount: 0, attendanceMode: undefined, ...{ childcareCount: 0, childcareAgesNote: "" } });
    expect(screen.getByRole("status")).toHaveTextContent("Saved: going, no childcare");
    expect(screen.getByRole("status")).toHaveFocus();
    expect(count).not.toHaveAttribute("aria-invalid");
    expect(screen.queryByRole("button", { name: "Clear childcare request" })).not.toBeInTheDocument();
  });

  it("prefills your request, focuses invalid counts, and leaves Maybe independent", async () => {
    vi.mocked(Events.register).mockResolvedValue({ people: [], registration: { ...registration, status: "maybe", childcareCount: 2, childcareAgesNote: "3 and 5" } });
    render(<SavedRsvp initial={occurrence({ childcareAvailable: true, myRegistration: "going", myChildcareCount: 2, myChildcareAgesNote: "3 and 5" })} />);
    const count = screen.getByLabelText(/^Children needing childcare/);
    expect(count).toHaveValue("2");
    await userEvent.clear(count);
    await userEvent.type(count, "abc");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(count).toHaveAttribute("aria-invalid", "true");
    expect(count).toHaveFocus();
    expect(Events.register).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Maybe" }));
    expect(Events.register).toHaveBeenCalledWith(7, { status: "maybe", date: "2026-12-01", guestCount: 0, attendanceMode: undefined });
    expect(screen.queryByText("Childcare for this date")).not.toBeInTheDocument();
  });

  it("disables a known full count while still saving Going without childcare", async () => {
    vi.mocked(Events.register).mockResolvedValue({ people: [], registration });
    render(<SavedRsvp initial={occurrence({ childcareAvailable: true, childcareFull: true })} />);
    await userEvent.click(screen.getByRole("button", { name: "Going" }));
    const count = screen.getByLabelText(/^Children needing childcare/);
    expect(count).toBeDisabled();
    expect(count).toHaveAccessibleDescription("Childcare is full for this date");
    expect(screen.getByRole("status")).toHaveTextContent("Saved: going, no childcare");
  });

  it("switches attendance using saved childcare rather than an unsaved draft", async () => {
    vi.mocked(Events.register).mockResolvedValue({ people: [], registration: { ...registration, attendanceMode: "remote", childcareCount: 2 } });
    render(<SavedRsvp initial={occurrence({ childcareAvailable: true, remoteAvailable: true, myRegistration: "going", myChildcareCount: 2 })} />);
    await userEvent.clear(screen.getByLabelText(/^Children needing childcare/));
    await userEvent.type(screen.getByLabelText(/^Children needing childcare/), "abc");
    await userEvent.click(screen.getByRole("radio", { name: "Remote" }));
    expect(Events.register).toHaveBeenCalledExactlyOnceWith(7, { status: "going", date: "2026-12-01", guestCount: 0, attendanceMode: "remote" });
    expect(screen.getByRole("status")).toHaveTextContent("Switched to remote");
  });

  it("announces a waitlist response without claiming Going was saved", async () => {
    vi.mocked(Events.register).mockResolvedValue({ people: [], registration: { ...registration, status: "waitlisted", waitlistPosition: 1 } });
    render(<SavedRsvp initial={occurrence({ childcareAvailable: true, waitlistEnabled: true })} />);
    await userEvent.click(screen.getByRole("button", { name: "Going" }));
    expect(screen.getByRole("button", { name: "Going" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.queryByText("Childcare for this date")).not.toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("You're on the in-person waitlist.");
  });
});

describe("named household answers", () => {
  beforeEach(() => vi.resetAllMocks());
  const kit: import("../types").HouseholdPerson = { id: 17, householdId: 1, canConvert: false, firstName: "Kit", lastName: "", name: "Kit", kind: "child", age: 6, photoConsent: false, linkStatus: "confirmed", registrationEligibility: "eligible", createdBy: 3 };
  const mary: import("../types").HouseholdPerson = { ...kit, id: 18, firstName: "Mary", name: "Mary", kind: "adult", age: 35 };
  function family(instance = occurrence({ remoteAvailable: true, childcareAvailable: true })) {
    return render(<MemoryRouter><RsvpButtons people={[kit, mary]} instance={instance} onChange={() => {}} /></MemoryRouter>);
  }

  it("can save only a child without changing Me and explicitly clears remote childcare", async () => {
    vi.mocked(Events.register).mockResolvedValue({ registration: null, people: [{ registrationId: 9, personId: 17, registeredBy: 3, status: "going", attendanceMode: "remote", waitlistPosition: null, childcareRequested: false }] });
    family();
    await userEvent.click(screen.getByRole("checkbox", { name: "Kit" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Childcare for Kit" }));
    await userEvent.click(screen.getByRole("radio", { name: "Remote" }));
    expect(screen.queryByRole("checkbox", { name: "Childcare for Kit" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Save answers" }));
    expect(Events.register).toHaveBeenCalledWith(7, { date: "2026-12-01", people: [{ personId: 17, status: "going", attendanceMode: "remote", childcareRequested: false }] });
    expect(screen.getByRole("status")).toHaveTextContent("Kit: going remotely");
  });

  it("ties a bad childcare count to its field and focuses it (PR 110 M2)", async () => {
    family();
    await userEvent.click(screen.getByRole("checkbox", { name: "Me" }));
    const count = screen.getByLabelText(/^Other children needing childcare/);
    await userEvent.clear(count); await userEvent.type(count, "-1");
    await userEvent.click(screen.getByRole("button", { name: "Save answers" }));
    expect(Events.register).not.toHaveBeenCalled();
    expect(count).toHaveAttribute("aria-invalid", "true");
    expect(count).toHaveFocus();
  });

  it("sends an explicit decline on unselection and keeps another person's mode independent", async () => {
    vi.mocked(Events.register).mockResolvedValue({ registration: null, people: [{ registrationId: 9, personId: 17, registeredBy: 3, status: "declined", attendanceMode: "in_person", waitlistPosition: null, childcareRequested: false }, { registrationId: 10, personId: 18, registeredBy: 3, status: "waitlisted", attendanceMode: "remote", waitlistPosition: 2, childcareRequested: false }] });
    family(occurrence({ remoteAvailable: true, myHouseholdRegistrations: [{ registrationId: 9, personId: 17, registeredBy: 3, holderName: "Holder", status: "going", attendanceMode: "in_person", waitlistPosition: null, childcareRequested: false, unavailable: false }] }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Kit" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Mary" }));
    await userEvent.click(screen.getByRole("radio", { name: "Remote" }));
    await userEvent.click(screen.getByRole("button", { name: "Save answers" }));
    expect(Events.register).toHaveBeenCalledWith(7, expect.objectContaining({ people: [{ personId: 17, status: "declined", attendanceMode: "in_person", childcareRequested: false }, { personId: 18, status: "going", attendanceMode: "remote", childcareRequested: false }] }));
    expect(screen.getByRole("status")).toHaveTextContent("Mary: waitlist position 2");
    expect(screen.getByRole("status")).not.toHaveTextContent("Mary: going");
  });

  it("keeps draft care and unnamed ages on failure without duplicating named children", async () => {
    vi.mocked(Events.register).mockRejectedValue(new ApiError(409, "private", { code: "childcare_full", personId: 17 }));
    family();
    await userEvent.click(screen.getByRole("checkbox", { name: "Me" }));
    await userEvent.type(screen.getByRole("textbox", { name: /^Ages/ }), "3 and 7");
    await userEvent.click(screen.getByRole("checkbox", { name: "Kit" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Childcare for Kit" }));
    await userEvent.click(screen.getByRole("button", { name: "Save answers" }));
    expect(Events.register).toHaveBeenCalledWith(7, expect.objectContaining({ childcareCount: 0, childcareAgesNote: "3 and 7", people: [expect.objectContaining({ childcareRequested: true })] }));
    expect(screen.getByRole("textbox", { name: /^Ages/ })).toHaveValue("3 and 7");
    expect(screen.getByRole("checkbox", { name: "Childcare for Kit" })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Kit" })).toHaveFocus();
    expect(screen.getByLabelText(/^Other children needing childcare/)).toHaveValue("0");
  });

  it("keeps pending and missing-age people visible but disabled even if Me was removed", () => {
    render(<MemoryRouter><RsvpButtons instance={occurrence({ myRegistration: "removed" })} people={[{ ...kit, registrationEligibility: "link_pending", linkStatus: "pending" }, { ...mary, registrationEligibility: "birth_date_required" }]} onChange={() => {}} /></MemoryRouter>);
    expect(screen.getByRole("checkbox", { name: "Kit" })).toBeDisabled();
    expect(screen.getByRole("checkbox", { name: "Mary" })).toBeDisabled();
    expect(screen.getByText("Waiting for an admin to confirm", { exact: false })).toBeInTheDocument();
    expect(screen.getByText(/Add a birth date in Profile/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save answers" })).toBeInTheDocument();
  });
});
