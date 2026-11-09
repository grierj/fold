import { render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { RemoteBadge } from "../RemoteBadge";
import { ChildcareBadge } from "../ChildcareBadge";

it("says remote attendance and childcare in words", () => {
  render(<><RemoteBadge /><ChildcareBadge /><ChildcareBadge count={2} /></>);
  expect(screen.getByText("Remote")).toBeInTheDocument();
  expect(screen.getByText("Childcare")).toBeInTheDocument();
  expect(screen.getByText("+2 childcare")).toBeInTheDocument();
});
