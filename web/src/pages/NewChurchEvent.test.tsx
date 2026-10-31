import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RouterProvider, createMemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { Events, SeriesRooms, Site } from "../api";
import { UnsavedChangesBlocker } from "../lib/unsavedChanges";
import { NewChurchEvent } from "./NewChurchEvent";

vi.mock("../auth", () => ({ useAuth: () => ({ user: { id: 1 }, loading: false, isAdmin: true }) }));
vi.mock("../api", async (importActual) => ({
  ...(await importActual<typeof import("../api")>()),
  Events: { createChurch: vi.fn(), generateJitsi: vi.fn() },
  SeriesRooms: { check: vi.fn() },
  Site: { get: vi.fn().mockResolvedValue({ timezone: "America/Denver" }) },
}));

function renderForm() {
  const router = createMemoryRouter(
    [
      { path: "/events/new", element: <><UnsavedChangesBlocker /><NewChurchEvent /></> },
      { path: "/events/:id", element: <p>Event page</p> },
      { path: "/elsewhere", element: <p>Elsewhere page</p> },
    ],
    { initialEntries: ["/events/new"] },
  );
  render(<RouterProvider router={router} />);
  return router;
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(Events.createChurch).mockResolvedValue({ event: { id: 4 } } as never);
  vi.mocked(Site.get).mockResolvedValue({ timezone: "America/Denver" } as never);
});

it("shares the group form's order: When, Location, then a closed More options with its summary", async () => {
  renderForm();
  await screen.findByText("Mountain time");
  expect(screen.getByRole("heading", { name: "When" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Just once" })).toHaveAttribute("aria-pressed", "true");
  expect(screen.getByRole("button", { name: "1½ hr" })).toHaveAttribute("aria-pressed", "true");
  const toggle = screen.getByRole("button", { name: "More options" });
  expect(toggle).toHaveAttribute("aria-expanded", "false");
  expect(screen.getByText("Visible to: Public · No seat limit · No video · No childcare · Reminder 1 day before")).toBeInTheDocument();
  // Church-wide events offer public or private only.
  await userEvent.click(toggle);
  expect(screen.getByRole("group", { name: "Who can see this event" })).toBeInTheDocument();
  expect(screen.queryByRole("radio", { name: /^Invite only/ })).not.toBeInTheDocument();
});

it("sends the seat limit, the other host and a weekly rule", async () => {
  const router = renderForm();
  await screen.findByText("Mountain time");
  await userEvent.type(screen.getByLabelText("Title"), "Potluck");
  await userEvent.type(screen.getByLabelText("Date"), "2026-12-02");
  await userEvent.click(screen.getByRole("button", { name: "Every week" }));
  await userEvent.type(screen.getByLabelText(/^Another host's email/), "lee@example.com");
  await userEvent.click(screen.getByRole("button", { name: "More options" }));
  await userEvent.type(screen.getByLabelText(/^Seat limit/), "40");
  await userEvent.click(screen.getByRole("button", { name: "Schedule event" }));
  await waitFor(() => expect(Events.createChurch).toHaveBeenCalled());
  expect(Events.createChurch).toHaveBeenCalledWith(expect.objectContaining({
    title: "Potluck", capacity: 40, waitlistEnabled: false, hosts: ["lee@example.com"],
    recurrenceRule: "FREQ=WEEKLY;BYDAY=WE", durationMinutes: 90,
  }));
  expect(await screen.findByText("Event page")).toBeInTheDocument();
  expect(router.state.location.pathname).toBe("/events/4");
});

it("sends the reminder time, 1 day before by default (M3.6b)", async () => {
  renderForm();
  await screen.findByText("Mountain time");
  await userEvent.type(screen.getByLabelText("Title"), "Potluck");
  await userEvent.type(screen.getByLabelText("Date"), "2026-12-02");
  await userEvent.click(screen.getByRole("button", { name: "More options" }));
  expect(screen.getByLabelText("Reminder email")).toHaveValue("1440");
  await userEvent.click(screen.getByRole("button", { name: "Schedule event" }));
  await waitFor(() => expect(Events.createChurch).toHaveBeenCalled());
  expect(Events.createChurch).toHaveBeenCalledWith(expect.objectContaining({ reminderEmailMinutes: 1440 }));
});

it("asks before leaving unsaved edits", async () => {
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  const router = renderForm();
  await screen.findByText("Mountain time");
  await userEvent.type(screen.getByLabelText("Title"), "Potluck");
  void router.navigate("/elsewhere");
  await waitFor(() => expect(confirm).toHaveBeenCalledWith("Leave without saving? Your changes will be lost."));
  expect(screen.queryByText("Elsewhere page")).not.toBeInTheDocument();
  confirm.mockRestore();
});

it("offers a Room beside Location when rooms are on and books it for the series on create", async () => {
  vi.mocked(Site.get).mockResolvedValue({ timezone: "America/Denver", roomsEnabled: true } as never);
  vi.mocked(SeriesRooms.check).mockResolvedValue({
    rooms: [{ id: 3, name: "Fellowship Hall", building: "Main", capacity: 80, features: "", restricted: false, busyDates: 0, tooSmall: false }],
    dates: 12,
  });
  renderForm();
  await screen.findByText("Mountain time");
  const room = await screen.findByRole("combobox", { name: /^Room/ });
  expect(room).toBeDisabled();
  expect(screen.getByRole("textbox", { name: /^Location/ }).compareDocumentPosition(room) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  expect(room.compareDocumentPosition(screen.getByRole("button", { name: "More options" })) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  await userEvent.type(screen.getByLabelText("Title"), "Potluck");
  await userEvent.type(screen.getByLabelText("Date"), "2026-12-02");
  await userEvent.click(screen.getByRole("button", { name: "Every week" }));
  await screen.findByRole("option", { name: "Fellowship Hall · Main (80 seats)" });
  // A church-wide event has no group, so the check sends none, and it sends what create sends,
  // with the visibility chosen for room suggestions.
  expect(SeriesRooms.check).toHaveBeenLastCalledWith({
    startsLocal: "2026-12-02T19:00", durationMinutes: 90, tzid: "America/Denver", recurrenceRule: "FREQ=WEEKLY;BYDAY=WE", capacity: null,
    visibility: "public",
  });
  await userEvent.selectOptions(room, "3");
  await userEvent.click(screen.getByRole("button", { name: "Schedule event" }));
  await waitFor(() => expect(Events.createChurch).toHaveBeenCalled());
  expect(Events.createChurch).toHaveBeenCalledWith(expect.objectContaining({ roomId: 3, recurrenceRule: "FREQ=WEEKLY;BYDAY=WE" }));
});

it("shows no Room field while rooms are off, and sends no roomId", async () => {
  renderForm();
  await screen.findByText("Mountain time");
  expect(screen.queryByRole("combobox", { name: /^Room/ })).not.toBeInTheDocument();
  await userEvent.type(screen.getByLabelText("Title"), "Potluck");
  await userEvent.type(screen.getByLabelText("Date"), "2026-12-02");
  await userEvent.click(screen.getByRole("button", { name: "Schedule event" }));
  await waitFor(() => expect(Events.createChurch).toHaveBeenCalled());
  expect(vi.mocked(Events.createChurch).mock.calls[0][0]).not.toHaveProperty("roomId");
  expect(SeriesRooms.check).not.toHaveBeenCalled();
});
