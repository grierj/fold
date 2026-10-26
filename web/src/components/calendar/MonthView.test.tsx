import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { monthGrid } from "../../lib/calendar";
import type { CalendarOccurrence } from "../../types";
import { MonthView } from "./MonthView";

const statuses: CalendarOccurrence["myStatus"][] = ["going", "maybe", null, "waitlisted"];
const occs: CalendarOccurrence[] = statuses.map((myStatus, index) => ({
  eventId: index + 1, date: "2026-10-21", startsAt: "2026-10-21T18:00:00Z", endsAt: "2026-10-21T19:00:00Z",
  title: `Event ${index + 1}`, location: "", churchWide: true, remoteAvailable: false, childcareAvailable: false,
  myStatus, ownScope: true,
}));

describe("MonthView", () => {
  it("names each day button with its event count and selects on click", async () => {
    const onSelect = vi.fn();
    render(
      <MemoryRouter>
        <MonthView weeks={monthGrid("2026-10-01")} month="2026-10" byDay={new Map([["2026-10-21", occs]])}
          today="2026-10-19" selected="2026-10-19" onSelect={onSelect} onNavigate={() => {}} caption="October 2026" />
      </MemoryRouter>
    );
    expect(screen.getByRole("table", { name: "October 2026" })).toBeInTheDocument();
    const [phone] = screen.getAllByRole("button", { name: "Wednesday, October 21, 4 events, 1 going, 1 maybe, 1 not answered, 1 waitlisted" });
    await userEvent.click(phone);
    expect(onSelect).toHaveBeenCalledWith("2026-10-21");
    expect(screen.getAllByRole("button", { name: /^Monday, October 19, today, 0 events$/ })[0]).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByText("Nothing on this day.")).toBeInTheDocument();
    const dots = phone.querySelectorAll("[aria-hidden=true] span");
    expect(dots).toHaveLength(3);
    expect(dots[0]).toHaveClass("bg-primary");
    expect(dots[1]).toHaveClass("bg-transparent", "border-primary");
    expect(dots[2]).toHaveClass("bg-transparent", "border-primary");
  });

  it("moves by arrow keys with one day in the Tab order, and turns the month off the grid", async () => {
    const onNavigate = vi.fn();
    render(
      <MemoryRouter>
        <MonthView weeks={monthGrid("2026-10-01")} month="2026-10" byDay={new Map()} today="2026-10-19"
          selected="2026-10-19" onSelect={() => {}} onNavigate={onNavigate} caption="October 2026" />
      </MemoryRouter>
    );
    const [start] = screen.getAllByRole("button", { name: /^Monday, October 19/ });
    expect(start).toHaveAttribute("tabindex", "0");
    expect(screen.getAllByRole("button", { name: /^Tuesday, October 20/ })[0]).toHaveAttribute("tabindex", "-1");
    start.focus();
    await userEvent.keyboard("{ArrowRight}");
    expect(document.activeElement?.getAttribute("data-day")).toBe("2026-10-20");
    await userEvent.keyboard("{PageDown}");
    expect(onNavigate).toHaveBeenCalledWith("2026-11-01");
  });
});
