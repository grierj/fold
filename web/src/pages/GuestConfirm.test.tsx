import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { expect, it, vi } from "vitest";
import { Guests } from "../api/events";
import { GuestConfirm } from "./GuestConfirm";
vi.mock("../api/events", async (actual) => ({ ...(await actual<typeof import("../api/events")>()), Guests: { confirm: vi.fn() } }));
it("links a verified guest to the exact public date, not its hidden series", async () => {
  vi.mocked(Guests.confirm).mockResolvedValue({ registration: { eventId: 7, occurrenceId: 51, date: "2026-12-01", title: "Welcome evening", hostedBy: "Church", status: "going", startsAt: "2026-12-01T19:00:00Z", location: "" }, manageToken: "manage" });
  render(<MemoryRouter initialEntries={["/guest/confirm?token=verified"]}><GuestConfirm /></MemoryRouter>);
  await userEvent.click(screen.getByRole("button", { name: "Confirm my registration" }));
  const view = await screen.findByRole("link", { name: "View the event" });
  expect(view).toHaveAttribute("href", "/events/7/dates/2026-12-01");
  expect(view).toHaveClass("btn-link");
  const manage = screen.getByRole("link", { name: "Manage or cancel" });
  expect(manage).toHaveAttribute("href", "/guest/manage/manage");
  expect(manage).toHaveClass("btn-secondary");
});
