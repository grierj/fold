import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { RsvpButtons } from "./RsvpButtons";
import { Events } from "../api";
import type { EventOccurrence } from "../types";

vi.mock("../api", () => ({
  ApiError: class extends Error {},
  Events: { register: vi.fn() },
}));

function occurrence(over: Partial<EventOccurrence>): EventOccurrence {
  return {
    id: 1,
    eventId: 7,
    date: "2026-12-01",
    startsAt: "2026-12-01T19:00:00Z",
    endsAt: "2026-12-01T20:00:00Z",
    startsLocal: "2026-12-01T19:00",
    endsLocal: "2026-12-01T20:00",
    tzid: "UTC",
    durationMinutes: 60,
    location: "Hall",
    title: "Supper",
    description: "",
    recurrenceRule: "",
    seriesStartsLocal: "2026-12-01T19:00",
    seriesTitle: "Supper",
    seriesLocation: "Hall",
    seriesDurationMinutes: 60,
    overridden: false,
    registrations: { going: 0, maybe: 0, declined: 0, waitlisted: 0 },
    myRegistration: null,
    capacity: null,
    seatsUsed: 0,
    spotsLeft: null,
    waitlistEnabled: false,
    guestsAllowed: 0,
    remoteAvailable: false,
    remoteCapacity: null,
    remoteSeatsUsed: 0,
    remoteSpotsLeft: null,
    videoProvider: "none",
    videoReveal: "registered_only",
    ...over,
  };
}

describe("RsvpButtons", () => {
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
    expect(screen.getByRole("button", { name: "In person" })).toHaveAttribute("aria-pressed", "true");
    await userEvent.click(screen.getByRole("button", { name: "Remote" }));
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
    expect(screen.getByRole("button", { name: "Remote" })).toHaveAttribute("aria-pressed", "true");
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
    await userEvent.click(screen.getByRole("button", { name: "Remote" }));
    expect(screen.getByText("This event is full.")).toBeInTheDocument();
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
