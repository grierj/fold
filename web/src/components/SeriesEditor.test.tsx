import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { SeriesEditor } from "./SeriesEditor";

vi.mock("../api", () => ({
  ApiError: class extends Error {},
  Events: { update: vi.fn() },
  Site: { get: vi.fn().mockResolvedValue({ timezone: "UTC" }) },
}));

describe("SeriesEditor", () => {
  it("carries the capacity, waitlist, and guest fields with their limit text", () => {
    render(
      <SeriesEditor
        series={{
          eventId: 7,
          startsLocal: "2026-12-01T19:00",
          durationMinutes: 60,
          tzid: "UTC",
          title: "Supper",
          description: "",
          location: "Hall",
          recurrenceRule: "",
          capacity: 20,
          waitlistEnabled: true,
          guestsAllowed: 2,
        }}
        idPrefix="test"
        onSaved={() => {}}
      />
    );
    const capacity = screen.getByLabelText(/^Capacity/) as HTMLInputElement;
    expect(capacity.value).toBe("20");
    expect(screen.getByText(/Seats per date/)).toBeInTheDocument();
    const guests = screen.getByLabelText("Guests allowed") as HTMLInputElement;
    expect(guests.value).toBe("2");
    expect(screen.getByText(/0–5/)).toBeInTheDocument();
    expect(screen.getByLabelText(/^Waitlist when full/)).toBeChecked();
    expect(screen.getByText(/refuses new registrations/)).toBeInTheDocument();
  });

  it("shows an empty capacity as no limit", () => {
    render(
      <SeriesEditor
        series={{
          eventId: 7,
          startsLocal: "2026-12-01T19:00",
          durationMinutes: 60,
          tzid: "UTC",
          title: "Supper",
          description: "",
          location: "Hall",
          recurrenceRule: "",
          capacity: null,
          waitlistEnabled: false,
          guestsAllowed: 0,
        }}
        idPrefix="test2"
        onSaved={() => {}}
      />
    );
    const capacity = screen.getByLabelText(/^Capacity/) as HTMLInputElement;
    expect(capacity.value).toBe("");
    expect(capacity).toHaveAttribute("placeholder", "No limit");
  });
});
