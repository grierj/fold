import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RouterProvider, createMemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { SeriesEditor } from "./SeriesEditor";
import { UnsavedChangesBlocker } from "../lib/unsavedChanges";
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
    const capacity = screen.getByLabelText(/^Seat limit/) as HTMLInputElement;
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
    const capacity = screen.getByLabelText(/^Seat limit/) as HTMLInputElement;
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

const baseSeries = {
  eventId: 7, startsLocal: "2026-12-02T19:00", durationMinutes: 90, tzid: "America/Denver",
  title: "Supper", description: "", location: "Hall", recurrenceRule: "FREQ=WEEKLY;BYDAY=WE",
  capacity: null, waitlistEnabled: false, guestsAllowed: 0,
};

describe("SeriesEditor form layout (DEC-95)", () => {
  it("shares the create forms' order, names the event's zone and opens More options", () => {
    render(<SeriesEditor series={baseSeries} idPrefix="order" onSaved={() => {}} />);
    expect(screen.getByRole("button", { name: "More options" })).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("Mountain time")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Every week" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("group", { name: "Repeat on" })).toBeInTheDocument();
    const form = document.querySelector("form")!;
    const order = ["Title", "Description", "When", "First date", "Repeats", "Location", "More options", "Seat limit", "Online", "Childcare"];
    const nodes = order.map((t) => Array.from(form.querySelectorAll("label, h2, h3, p, button")).find((n) => n.textContent?.trim().startsWith(t))!);
    nodes.forEach((n, i) => {
      expect(n, order[i]).toBeTruthy();
      if (i > 0) expect(nodes[i - 1].compareDocumentPosition(n) & Node.DOCUMENT_POSITION_FOLLOWING, order[i]).toBeTruthy();
    });
  });

  it("opens on Other, with its minutes, when the length is not 1, 1½ or 2 hours", async () => {
    vi.mocked(Events.update).mockClear();
    render(<SeriesEditor series={{ ...baseSeries, durationMinutes: 45 }} idPrefix="other" onSaved={() => {}} />);
    expect(screen.getByRole("button", { name: "Other" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByLabelText("Duration (minutes)")).toHaveValue(45);
    await userEvent.click(screen.getByRole("button", { name: "2 hr" }));
    await userEvent.click(screen.getByRole("button", { name: "Save series" }));
    expect(Events.update).toHaveBeenCalledWith(7, expect.objectContaining({ durationMinutes: 120, tzid: "America/Denver" }));
  });

  it("leaves the seat fields out of the body until they change", async () => {
    vi.mocked(Events.update).mockClear();
    render(<SeriesEditor series={baseSeries} idPrefix="seats0" onSaved={() => {}} />);
    await userEvent.click(screen.getByRole("button", { name: "Save series" }));
    const untouched = vi.mocked(Events.update).mock.calls[0][1] as Record<string, unknown>;
    expect(untouched).not.toHaveProperty("capacity");
    expect(untouched).not.toHaveProperty("waitlistEnabled");
  });

  it("sends the seat limit and waitlist once changed", async () => {
    vi.mocked(Events.update).mockClear();
    render(<SeriesEditor series={baseSeries} idPrefix="seats" onSaved={() => {}} />);
    await userEvent.type(screen.getByLabelText(/^Seat limit/), "12");
    await userEvent.click(screen.getByLabelText(/^Waitlist when full/));
    await userEvent.click(screen.getByRole("button", { name: "Save series" }));
    expect(Events.update).toHaveBeenLastCalledWith(7, expect.objectContaining({ capacity: 12, waitlistEnabled: true }));
  });

  it("opens a closed More options and focuses the field with the problem", async () => {
    vi.mocked(Events.update).mockClear();
    render(<SeriesEditor series={{ ...baseSeries, remoteAvailable: true, remoteCapacity: 0 }} idPrefix="closed" onSaved={() => {}} />);
    const toggle = screen.getByRole("button", { name: "More options" });
    await userEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    await userEvent.click(screen.getByRole("button", { name: "Save series" }));
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    const cap = screen.getByLabelText(/^Remote seat limit/);
    await waitFor(() => expect(cap).toHaveFocus());
    expect(Events.update).not.toHaveBeenCalled();
  });

  it("asks before leaving unsaved edits, and not after a save", async () => {
    vi.mocked(Events.update).mockReset().mockResolvedValue({} as never);
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const router = createMemoryRouter(
      [
        { path: "/", element: <><UnsavedChangesBlocker /><SeriesEditor series={baseSeries} idPrefix="guard" onSaved={() => { void router.navigate("/done"); }} /></> },
        { path: "/elsewhere", element: <p>Elsewhere page</p> },
        { path: "/done", element: <p>Done page</p> },
      ],
    );
    render(<RouterProvider router={router} />);
    await userEvent.type(screen.getByRole("textbox", { name: "Title" }), "!");
    void router.navigate("/elsewhere");
    await waitFor(() => expect(confirm).toHaveBeenCalledWith("Leave without saving? Your changes will be lost."));
    expect(screen.queryByText("Elsewhere page")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Save series" }));
    expect(await screen.findByText("Done page")).toBeInTheDocument();
    expect(confirm).toHaveBeenCalledTimes(1);
    confirm.mockRestore();
  });
});
