import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { ViewSwitch } from "./ViewSwitch";

describe("ViewSwitch", () => {
  it("renders links and marks only the current one", () => {
    render(
      <MemoryRouter>
        <ViewSwitch
          label="Calendar view"
          items={[
            { label: "Agenda", to: "/calendar", current: false },
            { label: "Week", to: "/calendar?view=week", current: true },
          ]}
        />
      </MemoryRouter>
    );
    expect(screen.getByRole("navigation", { name: "Calendar view" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Week" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Agenda" })).not.toHaveAttribute("aria-current");
    expect(screen.getByRole("link", { name: "Agenda" })).toHaveAttribute("href", "/calendar");
  });
});
