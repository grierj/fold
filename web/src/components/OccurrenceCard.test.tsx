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
