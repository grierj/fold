import { render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { OccurrenceCard } from "./OccurrenceCard";
import { Button } from "./ui/Button";

it("keeps More beside the heading while the answer gets a separate full-width body", () => {
  render(<OccurrenceCard at="2026-12-01T19:00:00Z" title="Study" meta={<p>7 PM</p>}
    notice={<p>Visibility changed for this date</p>} menu={<Button variant="ghost">More</Button>}>
    <p>Attendance and answers</p>
  </OccurrenceCard>);
  const heading = screen.getByRole("heading", { name: "Study", level: 3 });
  const menu = screen.getByRole("button", { name: "More" });
  const card = screen.getByRole("article", { name: "Study" });
  const body = screen.getByText("Attendance and answers").parentElement!;
  expect(heading.parentElement).toContainElement(menu);
  expect(body).not.toContainElement(menu);
  expect(body).not.toContainElement(heading);
  expect(body).not.toHaveClass("grow", "flex");
  expect(body.parentElement).toBe(card);
  expect(screen.getByText("Visibility changed for this date").parentElement).toHaveClass("grow", "min-w-0");
});

it("compact drops the tile and visible heading but keeps the name, notice and menu", () => {
  render(<OccurrenceCard at="2026-12-01T19:00:00Z" title="Study" meta={null} compact
    notice={<p>Visibility changed for this date</p>} menu={<Button variant="ghost">More</Button>}>
    <p>Attendance and answers</p>
  </OccurrenceCard>);
  expect(screen.getByRole("article", { name: "Study" })).toBeInTheDocument();
  expect(screen.getByRole("heading", { name: "Study", level: 3 })).toHaveClass("sr-only");
  expect(screen.queryByText("Dec")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "More" })).toBeInTheDocument();
  expect(screen.getByText("Visibility changed for this date")).toBeInTheDocument();
  expect(screen.getByText("Attendance and answers")).toBeInTheDocument();
});

it.each([false, true])("can sit directly under the page heading when compact=%s", (compact) => {
  render(<OccurrenceCard at="2026-12-01T19:00:00Z" title="Study" meta={null} compact={compact} headingLevel={2}>
    <p>Attendance and answers</p>
  </OccurrenceCard>);
  expect(screen.getByRole("heading", { name: "Study", level: 2 })).toBeInTheDocument();
  expect(screen.getByRole("article", { name: "Study" })).toBeInTheDocument();
});

it("compact has no top row when there is nothing for it", () => {
  render(<OccurrenceCard at="2026-12-01T19:00:00Z" title="Study" meta={null} compact>
    <p>Attendance and answers</p>
  </OccurrenceCard>);
  expect(screen.getByText("Attendance and answers").parentElement?.parentElement?.children).toHaveLength(2);
});
