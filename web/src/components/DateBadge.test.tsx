import { render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { DateBadge } from "./DateBadge";

it.each(["sm", "md"] as const)("keeps the month label at the 12px token in a %s badge", (size) => {
  render(<DateBadge at="2026-12-01T18:00:00Z" size={size} />);
  expect(screen.getByText("Dec")).toHaveClass("text-xs");
});
