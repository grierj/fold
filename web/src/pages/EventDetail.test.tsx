import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Events, Households } from "../api";
import type { EventDetailResponse } from "../types";
import { occurrence } from "../test/occurrence";
import { EventDetail } from "./EventDetail";

vi.mock("../auth", () => ({ useAuth: () => ({ user: { id: 2 }, loading: false, isAdmin: false }) }));
vi.mock("../api", async (importActual) => ({
  ...(await importActual<typeof import("../api")>()),
  ApiError: (await importActual<typeof import("../api")>()).ApiError,
  Households: { mine: vi.fn().mockResolvedValue({ household: null }) },
  Events: { get: vi.fn(), updateOccurrence: vi.fn() },
}));

const page: EventDetailResponse = {
  event: {
    id: 7, title: "Hybrid study", description: "", startsLocal: "2026-12-01T19:00", durationMinutes: 60,
    tzid: "UTC", startsAt: "2026-12-01T19:00:00Z", endsAt: "2026-12-01T20:00:00Z", location: "Hall",
    recurrenceRule: "", visibility: "public", leaderOpened: false, churchWide: false,
    capacity: null, waitlistEnabled: false, guestsAllowed: 0,
    childcareAvailable: false, childcareCapacity: null, childcareAges: "", childcareNotes: "",
    remoteAvailable: true, remoteCapacity: 5, videoProvider: "none", videoReveal: "registered_only",
  },
  occurrences: [occurrence({
    remoteAvailable: true, remoteCapacity: 5, remoteSpotsLeft: 4,
    attendeesVisible: true,
    attendees: [{ registrationId: 31, userId: 3, name: "Sam", status: "going", guestCount: 0, attendanceMode: "remote" }],
  })],
  canManage: true, canSetVisibility: false,
};

function renderPage() {
  render(<MemoryRouter initialEntries={["/events/7"]}><Routes><Route path="/events/:id" element={<EventDetail />} /></Routes></MemoryRouter>);
}

async function editDate() {
  renderPage();
  await userEvent.click(await screen.findByRole("button", { name: "More for 2026-12-01" }));
  await userEvent.click(screen.getByRole("menuitem", { name: "Edit this date" }));
  return screen.getByRole("textbox", { name: /^Remote seat limit for this date/ });
}

describe("EventDetail remote seats", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    vi.mocked(Households.mine).mockResolvedValue({ household: null });
    vi.mocked(Events.get).mockResolvedValue(page);
    vi.mocked(Events.updateOccurrence).mockResolvedValue({ occurrence: page.occurrences[0] });
  });

  it.each(["1e", "-2", "1.5"])("preserves and focuses invalid per-date text %s without saving", async (text) => {
    const input = await editDate();
    expect(input).toHaveAttribute("inputmode", "numeric");
    await userEvent.clear(input);
    await userEvent.type(input, text);
    await userEvent.click(screen.getByRole("button", { name: "Save this date" }));
    expect(Events.updateOccurrence).not.toHaveBeenCalled();
    expect(input).toHaveValue(text);
    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(input).toHaveAccessibleDescription(expect.stringContaining("whole number from 0"));
    await waitFor(() => expect(input).toHaveFocus());
  });

  it.each([["0", 0], ["8", 8], ["", null]] as const)("saves the per-date limit %s", async (text, limit) => {
    const input = await editDate();
    await userEvent.clear(input);
    if (text) await userEvent.type(input, text);
    await userEvent.click(screen.getByRole("button", { name: "Save this date" }));
    expect(Events.updateOccurrence).toHaveBeenCalledWith(7, "2026-12-01", { remoteCapacityOverride: limit });
  });

  it("marks remote people in the leader roster", async () => {
    renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "More for 2026-12-01" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Manage sign-ups" }));
    const roster = within(screen.getByRole("region", { name: "Manage sign-ups" }));
    expect(roster.getByText("Sam", { selector: "span:not(.sr-only)" })).toBeInTheDocument();
    expect(roster.getByText("Remote")).toHaveClass("bg-sunken", "text-text-muted");
  });
});

it("shows the public childcare ages and notes", async () => {
  vi.mocked(Events.get).mockResolvedValue({ ...page, event: { ...page.event, childcareAvailable: true, childcareAges: "2–5", childcareNotes: "Check in at the nursery." } });
  renderPage();
  expect(await screen.findByText("Childcare available · 2–5")).toBeInTheDocument();
  expect(screen.getByText("Check in at the nursery.")).toBeInTheDocument();
});
