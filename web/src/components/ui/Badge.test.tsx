import { render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { Badge } from "./Badge";
import { RemoteBadge } from "../RemoteBadge";
import { ChildcareBadge } from "../ChildcareBadge";

it("uses the same neutral badge for remote attendance and childcare", () => {
  render(<><Badge>Example</Badge><RemoteBadge /><ChildcareBadge /><ChildcareBadge count={2} /></>);
  const shared = screen.getByText("Example").className;
  for (const label of ["Remote", "Childcare", "+2 childcare"]) {
    expect(screen.getByText(label).className).toBe(shared);
    expect(screen.getByText(label)).toHaveClass("bg-sunken", "text-text-muted");
  }
});
