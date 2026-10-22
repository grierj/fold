import { render, screen, waitFor } from "@testing-library/react";
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
        status: "going", guestCount: 0, waitlistPosition: null, attendanceMode: "remote",
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
    expect(Events.register).toHaveBeenCalledWith(7, "going", "2026-12-01", 0, "remote");
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
    const registration = { userId: 3, eventId: 7, occurrenceId: 1, date: "2026-12-01", status: "going" as const, guestCount: 1, waitlistPosition: null, attendanceMode: after };
    vi.mocked(Events.register).mockResolvedValue({ registration });
    const onChange = vi.fn();
    render(<RsvpButtons instance={occurrence({ remoteAvailable: true, myRegistration: "going", myAttendanceMode: before, myGuestCount: 1 })} onChange={onChange} />);
    expect(screen.getByRole("group", { name: "How you'll attend" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("radio", { name: label }));
    expect(Events.register).toHaveBeenCalledExactlyOnceWith(7, "going", "2026-12-01", 1, after);
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
    vi.mocked(Events.register).mockResolvedValue({ registration: { userId: 3, eventId: 7, occurrenceId: 1, date: "2026-12-01", status: "waitlisted", guestCount: 0, waitlistPosition: 2, attendanceMode: "remote" } });
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
    vi.mocked(Events.register).mockResolvedValue({ registration: { userId: 3, eventId: 7, occurrenceId: 1, date: "2026-12-01", status: "going", guestCount: 0, waitlistPosition: null, attendanceMode: "in_person" } });
    render(<RsvpButtons instance={occurrence({ remoteAvailable: true, remoteCapacity: 0, remoteSpotsLeft: 0, myAttendanceMode: "remote" })} onChange={() => {}} />);
    expect(screen.queryByRole("radio", { name: "Remote" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Going" }));
    expect(Events.register).toHaveBeenCalledExactlyOnceWith(7, "going", "2026-12-01", 0, "in_person");
  });
});
