import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { Tabs } from "./Tabs";

function Example({ variant }: { variant?: "chips" | "underline" }) {
  const [value, setValue] = useState("events");
  return <Tabs label="Group sections" value={value} onChange={setValue} variant={variant} options={[
    { id: "events", label: "Events" }, { id: "about", label: "About" }, { id: "members", label: "Members" },
  ]}>{value} content</Tabs>;
}

it.each(["chips", "underline"] as const)("links %s tabs and panels with one tab stop and wrapping arrow navigation", async (variant) => {
  render(<Example variant={variant} />);
  const user = userEvent.setup();
  await user.tab();
  const events = screen.getByRole("tab", { name: "Events" });
  const about = screen.getByRole("tab", { name: "About" });
  const members = screen.getByRole("tab", { name: "Members" });
  expect(events).toHaveFocus();
  expect(events).toHaveAttribute("aria-selected", "true");
  expect(about).toHaveAttribute("tabindex", "-1");
  await user.keyboard("{ArrowRight}");
  expect(about).toHaveFocus();
  expect(about).toHaveAttribute("aria-selected", "true");
  const panel = screen.getByRole("tabpanel", { name: "About" });
  expect(about).toHaveAttribute("aria-controls", panel.id);
  expect(panel).toHaveTextContent("about content");
  await user.tab();
  expect(panel).toHaveFocus();
  about.focus();
  await user.keyboard("{End}");
  expect(members).toHaveFocus();
  await user.keyboard("{ArrowRight}");
  expect(events).toHaveFocus();
  await user.keyboard("{ArrowLeft}");
  expect(members).toHaveFocus();
  await user.keyboard("{Home}");
  expect(events).toHaveFocus();
  expect(screen.getByRole("navigation")).toHaveClass("overflow-x-auto");
});
