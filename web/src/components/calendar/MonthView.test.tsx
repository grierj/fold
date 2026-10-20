import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { monthGrid } from "../../lib/calendar";
import type { CalendarOccurrence } from "../../types";
import { MonthView } from "./MonthView";

const occs: CalendarOccurrence[] = [1, 2].map((n) => ({
  eventId: n, date: "2026-10-21", startsAt: "2026-10-21T18:00:00Z", endsAt: "2026-10-21T19:00:00Z",
  title: `Event ${n}`, location: "", churchWide: true, myStatus: null,
}));

describe("MonthView", () => {
  it("names each day button with its event count and selects on click", async () => {
    const onSelect = vi.fn();
    render(
      <MemoryRouter>
        <MonthView weeks={monthGrid("2026-10-01")} month="2026-10" byDay={new Map([["2026-10-21", occs]])}
          today="2026-10-19" selected="2026-10-19" onSelect={onSelect} caption="October 2026" />
      </MemoryRouter>
    );
    expect(screen.getByRole("table", { name: "October 2026" })).toBeInTheDocument();
    const [phone] = screen.getAllByRole("button", { name: "Wednesday, October 21, 2 events" });
    await userEvent.click(phone);
    expect(onSelect).toHaveBeenCalledWith("2026-10-21");
    expect(screen.getAllByRole("button", { name: /^Monday, October 19, today, 0 events$/ })[0]).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByText("Nothing on this day.")).toBeInTheDocument();
  });
});
