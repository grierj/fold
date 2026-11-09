import { render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { DateBadge } from "./DateBadge";

it.each(["sm", "md"] as const)("shows the month and day in a %s badge", (size) => {
  render(<DateBadge at="2026-12-01T18:00:00Z" size={size} />);
  expect(screen.getByText("Dec")).toBeInTheDocument();
  expect(screen.getByText("1")).toBeInTheDocument();
});

it("dates the instant in the viewer's zone, so it agrees with the time beside it", () => {
  // 03:00 UTC on Dec 2 is still the evening of Dec 1 in the test zone (America/Boise).
  render(<DateBadge at="2026-12-02T03:00:00Z" />);
  expect(screen.getByText("Dec")).toBeInTheDocument();
  expect(screen.getByText("1")).toBeInTheDocument();
});
