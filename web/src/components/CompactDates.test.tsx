import { render, screen, within, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { occurrence } from "../test/occurrence";
import { CompactDates } from "./CompactDates";
import { addDays } from "../lib/calendar";
const dates = Array.from({ length: 40 }, (_, i) => {
  const date = addDays("2026-12-01", i);
  return occurrence({ id: i + 1, title: `Gathering ${i + 1}`, date, startsAt: `${date}T18:00:00Z`, endsAt: `${date}T19:00:00Z` });
});
const child = (d: typeof dates[number]) => <input aria-label={`Draft ${d.id}`} defaultValue="" />;
it("keeps a small schedule's direct answers", () => {
  render(<CompactDates dates={dates.slice(0, 4)}>{child}</CompactDates>);
  expect(screen.getAllByRole("textbox")).toHaveLength(4);
  expect(screen.queryByRole("button", { name: /Details and RSVP/ })).not.toBeInTheDocument();
});
it("pages long schedules and preserves a draft while another date is expanded", async () => {
  render(<CompactDates dates={dates}>{child}</CompactDates>);
  expect(screen.getAllByRole("textbox")).toHaveLength(1);
  expect(screen.getAllByRole("button", { name: /^Details and RSVP/ })).toHaveLength(11);
  expect(screen.getAllByRole("button", { name: /^Details and RSVP/ })[0]).toHaveClass("shrink-0", "whitespace-nowrap", "min-h-11");
  await userEvent.type(screen.getByLabelText("Draft 1"), "My note");
  await userEvent.click(screen.getByRole("button", { name: /^Details and RSVP: Gathering 2,/ }));
  expect(screen.getAllByRole("textbox")).toHaveLength(1);
  await userEvent.click(screen.getByRole("button", { name: /^Details and RSVP: Gathering 1,/ }));
  expect(screen.getByLabelText("Draft 1")).toHaveValue("My note");
  await userEvent.click(screen.getByRole("button", { name: "Next dates" }));
  expect(screen.getByRole("status")).toHaveTextContent("Showing 13 to 24 of 40 dates");
});
it("preserves every mounted draft while crossing the compact threshold both ways", async () => {
  const view = (n: number) => <CompactDates dates={dates.slice(0, n)}>{child}</CompactDates>;
  const rendered = render(view(6));
  await userEvent.type(screen.getByLabelText("Draft 6"), "Keep this sixth draft");
  rendered.rerender(view(7));
  await userEvent.click(screen.getByRole("button", { name: /^Details and RSVP: Gathering 6,/ }));
  expect(screen.getByLabelText("Draft 6")).toHaveValue("Keep this sixth draft");
  await userEvent.type(screen.getByLabelText("Draft 6"), " edited");
  rendered.rerender(view(6));
  expect(screen.getByLabelText("Draft 6")).toHaveValue("Keep this sixth draft edited");
  expect(screen.getAllByRole("textbox")).toHaveLength(6);
});

it("shows and searches a confirmed room and building before typed directions", async () => {
  render(<CompactDates dates={dates.map((d, index) => index === 4 ? { ...d, room: { id: 9, name: "Fellowship Hall", building: "Main" }, location: "north door" } : d)}>{child}</CompactDates>);
  expect(screen.getByText("Fellowship Hall · Main · north door")).toBeVisible();
  await userEvent.type(screen.getByRole("searchbox"), "Fellowship Hall");
  expect(screen.getByRole("status")).toHaveTextContent("Showing 1 to 1 of 1 dates");
  expect(screen.getByRole("button", { name: /^Details and RSVP: Gathering 5,/ })).toBeVisible();
});

it("does not expand sibling events that share the same date", async () => {
  render(<CompactDates dates={dates.map((d) => ({ ...d, date: dates[0].date }))}>{child}</CompactDates>);
  await userEvent.click(screen.getByRole("button", { name: /^Details and RSVP: Gathering 2,/ }));
  expect(screen.getAllByRole("textbox")).toHaveLength(1);
  expect(screen.getByRole("textbox")).toHaveAccessibleName("Draft 2");
});
it("opens a requested date beyond the first page, even after searching", async () => {
  const view = (requested?: string) => <CompactDates dates={dates} requested={requested}>{child}</CompactDates>;
  const rendered = render(view());
  await userEvent.type(screen.getByRole("searchbox"), "Gathering 1");
  rendered.rerender(view(dates[39].date));
  await waitFor(() => expect(screen.getByLabelText("Draft 40")).toBeVisible());
  expect(screen.getByRole("searchbox")).toHaveValue("");
  expect(screen.getByRole("status")).toHaveTextContent("Showing 37 to 40 of 40 dates");
  expect(within(screen.getByRole("navigation", { name: "dates pages" })).getByRole("button", { name: "Next dates" })).toBeDisabled();
});
