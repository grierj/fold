import { render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { HouseholdSkeleton } from "./HouseholdSkeleton";

it("reserves card space while the chunk or data is loading", () => {
  render(<HouseholdSkeleton />);
  expect(screen.getByRole("heading", { name: "My household" })).toBeInTheDocument();
  expect(screen.getByRole("region", { name: "My household loading" })).toHaveClass("min-h-80", "mt-6");
  expect(screen.getByRole("status")).toHaveTextContent("Loading your household");
});
