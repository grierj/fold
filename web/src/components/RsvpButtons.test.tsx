import { useState } from "react";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { RsvpButtons } from "./RsvpButtons";
import { ApiError, Events } from "../api";
import { occurrence } from "../test/occurrence";

vi.mock("../api", async (importActual) => ({
  ApiError: (await importActual<typeof import("../api")>()).ApiError,
  Events: { register: vi.fn() },
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
    vi.mocked(Events.register).mockResolvedValue({
      registration: {
        userId: 3, eventId: 7, occurrenceId: 1, date: "2026-12-01",
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
    expect(Events.register).toHaveBeenCalledWith(7, "going", "2026-12-01", 0, "remote", undefined);
    expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ attendanceMode: "remote" }));
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
    const registration = { userId: 3, eventId: 7, occurrenceId: 1, date: "2026-12-01", status: "going" as const, guestCount: 1, childcareCount: 0, childcareAgesNote: "", waitlistPosition: null, attendanceMode: after };
    vi.mocked(Events.register).mockResolvedValue({ registration });
    const onChange = vi.fn();
    render(<RsvpButtons instance={occurrence({ remoteAvailable: true, myRegistration: "going", myAttendanceMode: before, myGuestCount: 1 })} onChange={onChange} />);
    expect(screen.getByRole("group", { name: "How you'll attend" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("radio", { name: label }));
    expect(Events.register).toHaveBeenCalledExactlyOnceWith(7, "going", "2026-12-01", 1, after, undefined);
    expect(onChange).toHaveBeenCalledWith(registration);
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
    vi.mocked(Events.register).mockResolvedValue({ registration: { userId: 3, eventId: 7, occurrenceId: 1, date: "2026-12-01", status: "waitlisted", guestCount: 0, childcareCount: 0, childcareAgesNote: "", waitlistPosition: 2, attendanceMode: "remote" } });
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
    vi.mocked(Events.register).mockResolvedValue({ registration: { userId: 3, eventId: 7, occurrenceId: 1, date: "2026-12-01", status: "going", guestCount: 0, childcareCount: 0, childcareAgesNote: "", waitlistPosition: null, attendanceMode: "in_person" } });
    render(<RsvpButtons instance={occurrence({ remoteAvailable: true, remoteCapacity: 0, remoteSpotsLeft: 0, myAttendanceMode: "remote" })} onChange={() => {}} />);
    expect(screen.queryByRole("radio", { name: "Remote" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Going" }));
    expect(Events.register).toHaveBeenCalledExactlyOnceWith(7, "going", "2026-12-01", 0, "in_person", undefined);
  });
});

describe("childcare RSVP", () => {
  beforeEach(() => vi.resetAllMocks());
  const registration = {
    userId: 3, eventId: 7, occurrenceId: 1, date: "2026-12-01", status: "going", guestCount: 0,
    waitlistPosition: null, attendanceMode: "in_person", childcareCount: 0, childcareAgesNote: "",
  } satisfies import("../types").RegistrationResponse;

  function SavedRsvp({ initial = occurrence({ childcareAvailable: true }) }) {
    const [instance, setInstance] = useState(initial);
    return <RsvpButtons instance={instance} onChange={(reg) => setInstance((old) => ({
      ...old, myRegistration: reg.status, myChildcareCount: reg.childcareCount,
      myChildcareAgesNote: reg.childcareAgesNote, myAttendanceMode: reg.attendanceMode,
    }))} />;
  }

  it("saves Going with zero children before showing it as selected", async () => {
    let complete = () => {};
    vi.mocked(Events.register).mockImplementation(() => new Promise((resolve) => {
      complete = () => resolve({ registration });
    }));
    render(<SavedRsvp />);
    expect(screen.queryByLabelText(/^Children needing childcare/)).not.toBeInTheDocument();
    const going = screen.getByRole("button", { name: "Going" });
    await userEvent.click(going);
    expect(Events.register).toHaveBeenCalledExactlyOnceWith(7, "going", "2026-12-01", 0, undefined,
      { childcareCount: 0, childcareAgesNote: "" });
    expect(going).toHaveAttribute("aria-pressed", "false");
    expect(screen.queryByText("Add childcare")).not.toBeInTheDocument();
    await act(async () => complete());
    expect(going).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByText("Add childcare")).toBeInTheDocument();
    expect(screen.getByLabelText(/^Children needing childcare/)).toHaveValue("0");
    expect(screen.getByRole("status")).toHaveTextContent("Saved: going, no childcare");
    expect(screen.getByRole("status")).toHaveFocus();
  });

  it("leaves Going unselected when the first save fails", async () => {
    vi.mocked(Events.register).mockRejectedValue(new Error("offline"));
    render(<SavedRsvp />);
    await userEvent.click(screen.getByRole("button", { name: "Going" }));
    expect(screen.getByRole("button", { name: "Going" })).toHaveAttribute("aria-pressed", "false");
    expect(screen.queryByText("Add childcare")).not.toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("We couldn't reach Fold.");
  });

  it.each([1, 2, 0])("saves %s children separately and announces the saved request", async (count) => {
    vi.mocked(Events.register).mockResolvedValue({ registration: { ...registration, childcareCount: count, childcareAgesNote: "3 and 7" } });
    render(<SavedRsvp initial={occurrence({ childcareAvailable: true, myRegistration: "going" })} />);
    const input = screen.getByLabelText(/^Children needing childcare/);
    await userEvent.clear(input);
    await userEvent.type(input, String(count));
    await userEvent.type(screen.getByLabelText(/^Ages/), " 3 and 7 ");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(Events.register).toHaveBeenCalledExactlyOnceWith(7, "going", "2026-12-01", 0, undefined,
      { childcareCount: count, childcareAgesNote: "3 and 7" });
    expect(screen.getByRole("status")).toHaveTextContent(count === 0 ? "Saved: going, no childcare"
      : count === 1 ? "Saved: going, 1 child in childcare" : "Saved: going, 2 children in childcare");
    expect(screen.getByRole("status")).toHaveFocus();
    expect(screen.getByRole("button", { name: "Save" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Going" })).toHaveAttribute("aria-pressed", "true");
  });

  it("ties a full response to the count and offers Go without childcare", async () => {
    vi.mocked(Events.register)
      .mockRejectedValueOnce(new ApiError(409, "childcare is full for this date", { code: "childcare_full" }))
      .mockResolvedValueOnce({ registration });
    render(<SavedRsvp initial={occurrence({ childcareAvailable: true, myRegistration: "going" })} />);
    const count = screen.getByLabelText(/^Children needing childcare/);
    await userEvent.clear(count);
    await userEvent.type(count, "2");
    await userEvent.type(screen.getByLabelText(/^Ages/), "3 and 7");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(count).toHaveValue("2");
    expect(count).toHaveAttribute("aria-invalid", "true");
    expect(count).toHaveAccessibleDescription(expect.stringContaining("Childcare is full for this date. You can still come."));
    expect(count).toHaveFocus();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByLabelText(/^Ages/)).toHaveValue("3 and 7");
    expect(screen.getByRole("button", { name: "Going" })).toHaveAttribute("aria-pressed", "true");
    await userEvent.click(screen.getByRole("button", { name: "Go without childcare" }));
    expect(Events.register).toHaveBeenLastCalledWith(7, "going", "2026-12-01", 0, undefined,
      { childcareCount: 0, childcareAgesNote: "" });
    expect(screen.getByRole("status")).toHaveTextContent("Saved: going, no childcare");
    expect(screen.getByRole("status")).toHaveFocus();
    expect(count).not.toHaveAttribute("aria-invalid");
    expect(screen.queryByRole("button", { name: "Go without childcare" })).not.toBeInTheDocument();
  });

  it("prefills your request, focuses invalid counts, and leaves Maybe independent", async () => {
    vi.mocked(Events.register).mockResolvedValue({ registration: { ...registration, status: "maybe", childcareCount: 2, childcareAgesNote: "3 and 5" } });
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
    expect(Events.register).toHaveBeenCalledWith(7, "maybe", "2026-12-01", 0, undefined, undefined);
    expect(screen.queryByText("Add childcare")).not.toBeInTheDocument();
  });

  it("disables a known full count while still saving Going without childcare", async () => {
    vi.mocked(Events.register).mockResolvedValue({ registration });
    render(<SavedRsvp initial={occurrence({ childcareAvailable: true, childcareFull: true })} />);
    await userEvent.click(screen.getByRole("button", { name: "Going" }));
    const count = screen.getByLabelText(/^Children needing childcare/);
    expect(count).toBeDisabled();
    expect(count).toHaveAccessibleDescription("Childcare is full for this date");
    expect(screen.getByRole("status")).toHaveTextContent("Saved: going, no childcare");
  });

  it("switches attendance using saved childcare rather than an unsaved draft", async () => {
    vi.mocked(Events.register).mockResolvedValue({ registration: { ...registration, attendanceMode: "remote", childcareCount: 2 } });
    render(<SavedRsvp initial={occurrence({ childcareAvailable: true, remoteAvailable: true, myRegistration: "going", myChildcareCount: 2 })} />);
    await userEvent.clear(screen.getByLabelText(/^Children needing childcare/));
    await userEvent.type(screen.getByLabelText(/^Children needing childcare/), "abc");
    await userEvent.click(screen.getByRole("radio", { name: "Remote" }));
    expect(Events.register).toHaveBeenCalledExactlyOnceWith(7, "going", "2026-12-01", 0, "remote", undefined);
    expect(screen.getByRole("status")).toHaveTextContent("Switched to remote");
  });

  it("announces a waitlist response without claiming Going was saved", async () => {
    vi.mocked(Events.register).mockResolvedValue({ registration: { ...registration, status: "waitlisted", waitlistPosition: 1 } });
    render(<SavedRsvp initial={occurrence({ childcareAvailable: true, waitlistEnabled: true })} />);
    await userEvent.click(screen.getByRole("button", { name: "Going" }));
    expect(screen.getByRole("button", { name: "Going" })).toHaveAttribute("aria-pressed", "false");
    expect(screen.queryByText("Add childcare")).not.toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("You're on the in-person waitlist.");
  });
});
