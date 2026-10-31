import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RouterProvider, createMemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Groups, SeriesRooms, Site } from "../api";
import { UnsavedChangesBlocker } from "../lib/unsavedChanges";
import { NewEvent } from "./NewEvent";

vi.mock("../auth", () => ({ useAuth: () => ({ user: { id: 2 }, loading: false, isAdmin: false }) }));
vi.mock("../api", async (importActual) => ({
  ...(await importActual<typeof import("../api")>()),
  Groups: {
    get: vi.fn(),
    createEvent: vi.fn(),
  },
  SeriesRooms: { check: vi.fn() },
  Site: { get: vi.fn().mockResolvedValue({ timezone: "America/Denver" }) },
  Events: { generateJitsi: vi.fn() },
}));

function renderForm() {
  const router = createMemoryRouter(
    [
      { path: "/groups/:id/events/new", element: <><UnsavedChangesBlocker /><NewEvent /></> },
      { path: "/groups/:id", element: <p>Group page</p> },
      { path: "/events/:id", element: <p>Event page</p> },
      { path: "/elsewhere", element: <p>Elsewhere page</p> },
    ],
    { initialEntries: ["/groups/5/events/new"] },
  );
  render(<RouterProvider router={router} />);
  return router;
}

async function ready() {
  await screen.findByRole("heading", { name: "Schedule an event" });
  await screen.findByText("Mountain time");
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(Groups.get).mockResolvedValue({
    group: { name: "Choir", location: "Hall", visibility: "public" },
    canManage: true,
  } as never);
  vi.mocked(Groups.createEvent).mockResolvedValue({ event: { id: 9 } } as never);
  vi.mocked(Site.get).mockResolvedValue({ timezone: "America/Denver" } as never);
});

describe("NewEvent", () => {
  it("puts the title and description first, then When, Location and More options", async () => {
    renderForm();
    await ready();
    const form = document.querySelector("form")!;
    const labels = ["Title", "Description", "When", "Date", "Start time", "Duration", "Repeats", "Location", "More options"];
    const found = labels.map((l) => {
      const el = Array.from(form.querySelectorAll("label, h2, p, button")).find((n) => n.textContent?.replace(/\s*\(optional\)/, "").trim().startsWith(l) && (l !== "Title" || n.tagName === "LABEL"));
      expect(el, l).toBeTruthy();
      return el!;
    });
    for (let i = 1; i < found.length; i++) {
      expect(found[i - 1].compareDocumentPosition(found[i]) & Node.DOCUMENT_POSITION_FOLLOWING, labels[i]).toBeTruthy();
    }
    expect(screen.getByText("Mountain time")).toBeInTheDocument();
  });

  it("starts with More options closed and a one-line summary of what is inside", async () => {
    renderForm();
    await ready();
    const toggle = screen.getByRole("button", { name: "More options" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.getByText("Visible to: Public · No seat limit · No video · No childcare · Reminder email 1 day before")).toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Who can see this event" })).not.toBeInTheDocument();
    await userEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("group", { name: "Who can see this event" })).toBeInTheDocument();
    expect(screen.getByLabelText(/^Seat limit/)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Online" })).toBeInTheDocument();
    expect(screen.getByLabelText(/^Remote attendance/)).toHaveAccessibleDescription("Add a video link above so remote people can join.");
    expect(screen.getByLabelText(/^Childcare available/)).toBeInTheDocument();
  });

  it("sends the seat limit and waitlist set at creation", async () => {
    renderForm();
    await ready();
    await userEvent.type(screen.getByLabelText("Date"), "2026-12-02");
    await userEvent.click(screen.getByRole("button", { name: "More options" }));
    await userEvent.type(screen.getByLabelText(/^Seat limit/), "12");
    await userEvent.click(screen.getByLabelText(/^Waitlist when full/));
    await userEvent.click(screen.getByRole("button", { name: "Schedule event" }));
    await waitFor(() => expect(Groups.createEvent).toHaveBeenCalled());
    expect(Groups.createEvent).toHaveBeenCalledWith("5", expect.objectContaining({
      capacity: 12, waitlistEnabled: true, durationMinutes: 90, tzid: "America/Denver", recurrenceRule: "",
    }));
    expect(await screen.findByText("Group page")).toBeInTheDocument();
  });

  it("sends the reminder time: 1 day before unless changed (M3.6b)", async () => {
    renderForm();
    await ready();
    await userEvent.type(screen.getByLabelText("Date"), "2026-12-02");
    await userEvent.click(screen.getByRole("button", { name: "More options" }));
    const select = screen.getByLabelText("Reminder email");
    expect(select).toHaveValue("1440");
    await userEvent.selectOptions(select, "1 week before");
    await userEvent.click(screen.getByRole("button", { name: "Schedule event" }));
    await waitFor(() => expect(Groups.createEvent).toHaveBeenCalled());
    expect(Groups.createEvent).toHaveBeenCalledWith("5", expect.objectContaining({ reminderEmailMinutes: 10080 }));
  });

  it("sends a weekly rule from Every week and the chosen duration preset", async () => {
    renderForm();
    await ready();
    await userEvent.type(screen.getByLabelText("Date"), "2026-12-02");
    await userEvent.click(screen.getByRole("button", { name: "2 hr" }));
    await userEvent.click(screen.getByRole("button", { name: "Every week" }));
    await userEvent.click(screen.getByRole("button", { name: "Schedule event" }));
    await waitFor(() => expect(Groups.createEvent).toHaveBeenCalled());
    expect(Groups.createEvent).toHaveBeenCalledWith("5", expect.objectContaining({
      durationMinutes: 120, recurrenceRule: "FREQ=WEEKLY;BYDAY=WE",
    }));
  });

  it("opens More options and focuses the field when a problem is inside it", async () => {
    renderForm();
    await ready();
    await userEvent.type(screen.getByLabelText("Date"), "2026-12-02");
    const toggle = screen.getByRole("button", { name: "More options" });
    await userEvent.click(toggle);
    await userEvent.click(screen.getByRole("radio", { name: /^Paste a link/ }));
    await userEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.getByText(/Video link/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Schedule event" }));
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    const link = screen.getByLabelText("Video link");
    await waitFor(() => expect(link).toHaveFocus());
    expect(link).toHaveAttribute("aria-invalid", "true");
    expect(Groups.createEvent).not.toHaveBeenCalled();
  });

  it("remembers a seat limit while More options is closed, and shows it in the summary", async () => {
    renderForm();
    await ready();
    await userEvent.type(screen.getByLabelText("Date"), "2026-12-02");
    const toggle = screen.getByRole("button", { name: "More options" });
    await userEvent.click(toggle);
    await userEvent.type(screen.getByLabelText(/^Seat limit/), "3");
    await userEvent.click(toggle);
    // The limit is still remembered while the section is closed.
    expect(screen.getByText(/3 seats/)).toBeInTheDocument();
  });

  it("asks before leaving unsaved edits, and not after a save", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const router = renderForm();
    await ready();
    await router.navigate("/elsewhere");
    expect(confirm).not.toHaveBeenCalled();
    await router.navigate("/groups/5/events/new");
    await ready();
    await userEvent.type(screen.getByLabelText(/^Title/), "Supper");
    void router.navigate("/elsewhere");
    await waitFor(() => expect(confirm).toHaveBeenCalledWith("Leave without saving? Your changes will be lost."));
    expect(screen.queryByText("Elsewhere page")).not.toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Date"), "2026-12-02");
    await userEvent.click(screen.getByRole("button", { name: "Schedule event" }));
    expect(await screen.findByText("Group page")).toBeInTheDocument();
    expect(confirm).toHaveBeenCalledTimes(1);
    confirm.mockRestore();
  });

  it("keeps the shared When pieces: the zone line opens the picker", async () => {
    renderForm();
    await ready();
    expect(screen.queryByRole("combobox", { name: "Time zone" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Change time zone" }));
    const zone = screen.getByRole("combobox", { name: "Time zone" });
    expect(zone).toHaveAccessibleDescription(/church's time zone/);
    expect(within(document.querySelector("form")!).queryByRole("button", { name: "Change time zone" })).not.toBeInTheDocument();
  });
  const hall = { id: 3, name: "Fellowship Hall", building: "Main", capacity: 80, features: "", restricted: false, busyDates: 0, tooSmall: false };
  const roomsOn = (rooms: typeof hall[], dates: number) => {
    vi.mocked(Site.get).mockResolvedValue({ timezone: "America/Denver", roomsEnabled: true } as never);
    vi.mocked(SeriesRooms.check).mockResolvedValue({ rooms, dates });
  };

  it("shows no Room field while rooms are off, and sends no roomId", async () => {
    renderForm();
    await ready();
    expect(screen.queryByRole("combobox", { name: /^Room/ })).not.toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Date"), "2026-12-02");
    await userEvent.click(screen.getByRole("button", { name: "Schedule event" }));
    await waitFor(() => expect(Groups.createEvent).toHaveBeenCalled());
    expect(vi.mocked(Groups.createEvent).mock.calls[0][1]).not.toHaveProperty("roomId");
    expect(SeriesRooms.check).not.toHaveBeenCalled();
  });

  it("puts Room beside Location, checks the dates with the group, and books the room on create", async () => {
    roomsOn([hall], 12);
    renderForm();
    await ready();
    const room = await screen.findByRole("combobox", { name: /^Room/ });
    expect(screen.getByRole("textbox", { name: /^Location/ }).compareDocumentPosition(room) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(room.compareDocumentPosition(screen.getByRole("button", { name: "More options" })) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(room).toBeDisabled();
    await userEvent.type(screen.getByLabelText("Date"), "2026-12-02");
    await userEvent.click(screen.getByRole("button", { name: "Every week" }));
    await screen.findByRole("option", { name: "Fellowship Hall · Main (80 seats)" });
    expect(SeriesRooms.check).toHaveBeenLastCalledWith({
      startsLocal: "2026-12-02T19:00", durationMinutes: 90, tzid: "America/Denver", recurrenceRule: "FREQ=WEEKLY;BYDAY=WE", capacity: null, groupId: 5,
      visibility: "public",
    });
    await userEvent.selectOptions(room, "3");
    await userEvent.click(screen.getByRole("button", { name: "Schedule event" }));
    await waitFor(() => expect(Groups.createEvent).toHaveBeenCalled());
    expect(Groups.createEvent).toHaveBeenCalledWith("5", expect.objectContaining({ roomId: 3, recurrenceRule: "FREQ=WEEKLY;BYDAY=WE" }));
    // Any chosen room goes to the event page, busy or not: a stale or pending check can't hide dates that need one.
    expect(await screen.findByText("Event page")).toBeInTheDocument();
  });

  it("checks again when the seat limit changes", async () => {
    roomsOn([hall], 1);
    renderForm();
    await ready();
    await userEvent.type(screen.getByLabelText("Date"), "2026-12-02");
    await screen.findByRole("option", { name: "Fellowship Hall · Main (80 seats)" });
    await userEvent.click(screen.getByRole("button", { name: "More options" }));
    await userEvent.type(screen.getByLabelText(/^Seat limit/), "12");
    await waitFor(() => expect(SeriesRooms.check).toHaveBeenLastCalledWith(expect.objectContaining({ capacity: 12, groupId: 5 })));
  });

  it("sends a busy room to the event page, where its dates that need a room are listed", async () => {
    roomsOn([{ ...hall, busyDates: 2 }], 12);
    renderForm();
    await ready();
    await userEvent.type(screen.getByLabelText("Date"), "2026-12-02");
    await userEvent.click(screen.getByRole("button", { name: "Every week" }));
    await userEvent.selectOptions(await screen.findByRole("combobox", { name: /^Room/ }), await screen.findByRole("option", { name: /in use on 2 of 12 dates/ }));
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("the event page will list the busy dates under “Dates that need a room”."));
    await userEvent.click(screen.getByRole("button", { name: "Schedule event" }));
    await waitFor(() => expect(Groups.createEvent).toHaveBeenCalled());
    expect(Groups.createEvent).toHaveBeenCalledWith("5", expect.objectContaining({ roomId: 3 }));
    expect(await screen.findByText("Event page")).toBeInTheDocument();
  });
});
