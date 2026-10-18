import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { RsvpButtons } from "./RsvpButtons";
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

  it("shows the guest count input when guests are allowed", () => {
    render(
      <RsvpButtons instance={occurrence({ guestsAllowed: 2, myRegistration: "going" })} onChange={() => {}} />
    );
    expect(screen.getByLabelText(/Extra guests/)).toBeInTheDocument();
    expect(screen.getByText(/Up to 2/)).toBeInTheDocument();
  });
});
