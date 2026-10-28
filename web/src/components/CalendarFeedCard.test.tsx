import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

const get = vi.fn();
const create = vi.fn();
const setScope = vi.fn();
vi.mock("../api", async (orig) => ({
  ...(await orig<typeof import("../api")>()),
  CalendarFeeds: { get: () => get(), create: (s: string) => create(s), setScope: (s: string) => setScope(s), revoke: vi.fn() },
}));

import { ApiError } from "../api";
import { CalendarFeedCard } from "./CalendarFeedCard";

const renderCard = () => render(<MemoryRouter><CalendarFeedCard /></MemoryRouter>);

describe("CalendarFeedCard (M17-REVIEW)", () => {
  beforeEach(() => {
    get.mockReset();
    create.mockReset();
    setScope.mockReset();
  });

  it("shows a skeleton while loading, not the no-link copy", () => {
    get.mockReturnValue(new Promise(() => {}));
    renderCard();
    expect(screen.getByText("Loading your calendar link…")).toBeInTheDocument();
    expect(screen.queryByText(/don.t have a calendar link/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Create calendar link" })).toBeNull();
  });

  it("a failed load offers Try again and never Create (M1)", async () => {
    get.mockRejectedValueOnce(new ApiError(500, "boom"));
    get.mockResolvedValueOnce({ calendarFeed: { hasToken: true, scope: "mine" } });
    renderCard();
    await userEvent.click(await screen.findByRole("button", { name: "Try again" }));
    expect(screen.queryByRole("button", { name: "Create calendar link" })).toBeNull();
    expect(await screen.findByRole("button", { name: "Make a new link" })).toBeInTheDocument();
  });

  it("a fresh link makes Copy the primary action (M4)", async () => {
    get.mockResolvedValue({ calendarFeed: { hasToken: false, scope: "mine" } });
    create.mockResolvedValue({ calendarFeed: { hasToken: true, scope: "mine", url: "https://x/cal/u/t.ics" } });
    renderCard();
    await userEvent.click(await screen.findByRole("button", { name: "Create calendar link" }));
    expect(await screen.findByRole("button", { name: "Copy link" })).toHaveClass("btn-primary");
    expect(screen.getByRole("button", { name: "Make a new link" })).toHaveClass("btn-secondary");
  });

  it("a failed scope save puts the radio back (N2)", async () => {
    get.mockResolvedValue({ calendarFeed: { hasToken: true, scope: "mine" } });
    setScope.mockRejectedValue(new ApiError(500, "boom"));
    renderCard();
    await userEvent.click(await screen.findByLabelText(/Everything I can see/));
    expect(await screen.findByLabelText(/My events/)).toBeChecked();
  });
});
