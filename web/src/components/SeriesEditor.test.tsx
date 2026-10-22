import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { SeriesEditor } from "./SeriesEditor";
import { Events } from "../api";

vi.mock("../api", () => ({
  ApiError: class extends Error {},
  Events: { update: vi.fn(), generateJitsi: vi.fn().mockResolvedValue({ url: "https://meet.jit.si/fold-abc123def4" }) },
  Site: { get: vi.fn().mockResolvedValue({ timezone: "UTC" }) },
}));

describe("SeriesEditor", () => {
  it.each(["1e", "-2", "0"])("focuses the remote seat limit error for %s and keeps the other fields", async (capacity) => {
    render(<SeriesEditor series={{
      eventId: 7, startsLocal: "2026-12-01T19:00", durationMinutes: 60, tzid: "UTC",
      title: "Supper", description: "", location: "Hall", recurrenceRule: "",
      capacity: null, waitlistEnabled: false, guestsAllowed: 0, remoteAvailable: true,
    }} idPrefix="invalid" onSaved={() => {}} />);
    const input = screen.getByRole("textbox", { name: /^Remote seat limit/ });
    await userEvent.type(input, capacity);
    await userEvent.click(screen.getByRole("button", { name: "Save series" }));
    expect(Events.update).not.toHaveBeenCalled();
    expect(input).toHaveValue(capacity);
    expect(input).toHaveAttribute("aria-invalid", "true");
    await waitFor(() => expect(input).toHaveFocus());
    expect(screen.getByRole("textbox", { name: "Title" })).toHaveValue("Supper");
  });

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
          remoteAvailable: true,
          remoteCapacity: 15,
          videoProvider: "link",
          videoUrl: "https://zoom.example.org/j/123",
          videoReveal: "registered_only",
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
    // Remote attendance carries its own cap, separate from the room.
    expect(screen.getByLabelText(/^Remote attendance/)).toBeChecked();
    expect((screen.getByLabelText(/^Remote seat limit/) as HTMLInputElement).value).toBe("15");
    // The Video section carries the series' link and who may see it.
    expect((screen.getByLabelText("Video link") as HTMLInputElement).value).toBe(
      "https://zoom.example.org/j/123"
    );
    expect((screen.getByLabelText("Who sees the link") as HTMLSelectElement).value).toBe("registered_only");
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

it("validates the childcare limit and saves the parent information", async () => {
  vi.mocked(Events.update).mockClear();
  render(<SeriesEditor series={{ eventId: 7, startsLocal: "2026-12-01T19:00", durationMinutes: 60, tzid: "UTC",
    title: "Family night", description: "", location: "Hall", recurrenceRule: "", capacity: null,
    waitlistEnabled: false, guestsAllowed: 0, childcareAvailable: true, childcareCapacity: 4, childcareAges: "2–5", childcareNotes: "Nursery",
  }} idPrefix="childcare" onSaved={() => {}} />);
  const cap = screen.getByLabelText(/^Childcare limit/);
  expect(cap).toHaveValue("4");
  await userEvent.clear(cap);
  await userEvent.type(cap, "0");
  await userEvent.click(screen.getByRole("button", { name: "Save series" }));
  await waitFor(() => expect(cap).toHaveFocus());
  expect(cap).toHaveAttribute("aria-invalid", "true");
  expect(Events.update).not.toHaveBeenCalled();
  await userEvent.clear(cap);
  await userEvent.type(cap, "5");
  await userEvent.click(screen.getByRole("button", { name: "Save series" }));
  expect(Events.update).toHaveBeenCalledWith(7, expect.objectContaining({ childcareAvailable: true, childcareCapacity: 5, childcareAges: "2–5", childcareNotes: "Nursery" }));
});
