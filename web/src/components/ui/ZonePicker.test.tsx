import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";

vi.mock("../../api", () => ({ Site: { get: () => Promise.resolve({ name: "c", timezone: "America/Los_Angeles", selfSignup: "off" }) } }));
vi.mock("../../lib/when", async (orig) => ({ ...(await orig<typeof import("../../lib/when")>()), browserTimeZone: () => "America/Boise" }));

import { Field } from "./Field";
import { ZonePicker } from "./ZonePicker";

function Harness({ initial = "America/Los_Angeles", church = false }: { initial?: string; church?: boolean }) {
  const [v, setV] = useState(initial);
  return (
    <>
      <Field label="Time zone" hint="Pick one.">
        <ZonePicker value={v} onChange={setV} forChurchSetting={church} />
      </Field>
      <output data-testid="value">{v}</output>
    </>
  );
}

const box = () => screen.getByRole("combobox", { name: "Time zone" });
const options = () => screen.getAllByRole("option").map((o) => o.textContent);

describe("ZonePicker", () => {
  it("shows the current choice in full at rest, wired to its label and hint", () => {
    render(<Harness />);
    expect(box()).toHaveValue("Los Angeles (Pacific)");
    expect(box()).toHaveAttribute("aria-expanded", "false");
    expect(box()).toHaveAccessibleDescription("Pick one.");
    expect(box()).toHaveAttribute("title", "Los Angeles (Pacific) · America/Los_Angeles");
  });

  it("lists Yours, then Church, then the US zones", async () => {
    render(<Harness />);
    await userEvent.click(box());
    await waitFor(() => expect(options()[1]).toMatch(/^Los Angeles \(Pacific\)Church/));
    expect(options()[0]).toMatch(/^Boise \(Mountain\)Yours/);
    expect(options()[2]).toMatch(/^New York \(Eastern\)/);
    expect(box()).toHaveAttribute("aria-expanded", "true");
    expect(box().getAttribute("aria-activedescendant")).toBe(screen.getAllByRole("option")[1].id);
  });

  it("filters by region name, picks with the keyboard, and closes", async () => {
    render(<Harness initial="America/Boise" />);
    await userEvent.click(box());
    await userEvent.keyboard("pac");
    expect(options()[0]).toMatch(/^Los Angeles \(Pacific\)/);
    expect(options().some((o) => /^Boise/.test(o ?? ""))).toBe(false);
    await userEvent.keyboard("{Enter}");
    expect(screen.getByTestId("value")).toHaveTextContent("America/Los_Angeles");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(box()).toHaveValue("Los Angeles (Pacific)");
  });

  it("moves with the arrow keys, and Esc keeps the old choice", async () => {
    render(<Harness initial="America/Boise" />);
    box().focus();
    await userEvent.keyboard("{ArrowDown}");
    expect(screen.getByRole("listbox")).toBeInTheDocument();
    await userEvent.keyboard("{ArrowDown}");
    const active = box().getAttribute("aria-activedescendant")!;
    expect(document.getElementById(active)).toHaveAttribute("aria-selected", "true");
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(screen.getByTestId("value")).toHaveTextContent("America/Boise");
    expect(box()).toHaveValue("Boise (Mountain)");
  });

  it("says so when nothing matches", async () => {
    render(<Harness />);
    await userEvent.click(box());
    await userEvent.keyboard("zzzz");
    expect(screen.getByText(/No time zone matches/)).toHaveAttribute("role", "status");
  });

  it("has no Church entry for the church setting itself", async () => {
    render(<Harness church />);
    await userEvent.click(box());
    await waitFor(() => expect(options()[0]).toMatch(/Yours/));
    expect(options().some((o) => /Church/.test(o ?? ""))).toBe(false);
  });
});
