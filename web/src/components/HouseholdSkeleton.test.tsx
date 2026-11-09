import { render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { HouseholdSkeleton } from "./HouseholdSkeleton";

it("holds the household's place with a heading while the chunk or data is loading", () => {
  render(<HouseholdSkeleton />);
  expect(screen.getByRole("heading", { name: "My household" })).toBeInTheDocument();
  expect(screen.getByRole("region", { name: "My household loading" })).toBeInTheDocument();
  expect(screen.getByRole("status")).toHaveTextContent("Loading your household");
});

it("drops its heading under a page that already has the h1", () => {
  render(<HouseholdSkeleton showHeading={false} />);
  expect(screen.queryByRole("heading", { name: "My household" })).not.toBeInTheDocument();
  expect(screen.getByRole("region", { name: "My household loading" })).toBeInTheDocument();
});
