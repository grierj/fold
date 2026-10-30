import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RouterProvider, createMemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { Events } from "../api";
import { NewChurchEvent } from "./NewChurchEvent";

vi.mock("../auth", () => ({ useAuth: () => ({ user: { id: 1 }, loading: false, isAdmin: true }) }));
vi.mock("../api", async (importActual) => ({
  ...(await importActual<typeof import("../api")>()),
  Events: { createChurch: vi.fn(), generateJitsi: vi.fn() },
  Site: { get: vi.fn().mockResolvedValue({ timezone: "America/Denver" }) },
}));

function renderForm() {
  const router = createMemoryRouter(
    [
      { path: "/events/new", element: <NewChurchEvent /> },
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
});

it("shares the group form's order: When, Location, then a closed More options with its summary", async () => {
  renderForm();
  await screen.findByText("Mountain time");
  expect(screen.getByRole("heading", { name: "When" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Just once" })).toHaveAttribute("aria-pressed", "true");
  expect(screen.getByRole("button", { name: "1½ hr" })).toHaveAttribute("aria-pressed", "true");
  const toggle = screen.getByRole("button", { name: "More options" });
  expect(toggle).toHaveAttribute("aria-expanded", "false");
  expect(screen.getByText("Visible to: Public · No seat limit · No video · No childcare")).toBeInTheDocument();
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
