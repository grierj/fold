import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { useState } from "react";
import { useWhenState, type WhenInit } from "../lib/useWhenState";
import { WhenFields } from "./WhenFields";

vi.mock("../api", () => ({ Site: { get: vi.fn().mockResolvedValue({ timezone: "America/Denver" }) } }));

function Harness({ init = {}, customRule, until }: { init?: Partial<WhenInit>; customRule?: boolean; until?: boolean }) {
  const when = useWhenState({ date: "2026-12-02", time: "19:00", duration: 90, recurring: false, days: [], ...init });
  const [tzid, setTzid] = useState("America/Denver");
  const [rule, setRule] = useState("FREQ=DAILY");
  const [end, setEnd] = useState("");
  return (
    <>
      <WhenFields
        idPrefix="t"
        when={when}
        tzid={tzid}
        onTzid={setTzid}
        zoneHint="Pick the zone."
        customRule={customRule ? { value: rule, onChange: setRule } : undefined}
        until={until ? { value: end, onChange: setEnd } : undefined}
      />
      <output data-testid="state">{JSON.stringify({ duration: when.duration, days: when.days, recurring: when.recurring, tzid })}</output>
    </>
  );
}

const state = () => JSON.parse(screen.getByTestId("state").textContent!);
const pressed = (name: string) => screen.getByRole("button", { name }).getAttribute("aria-pressed");

describe("WhenFields", () => {
  it("offers 1 hr, 1½ hr, 2 hr and Other, starting on the current length", async () => {
    render(<Harness />);
    expect(["1 hr", "1½ hr", "2 hr", "Other"].map(pressed)).toEqual(["false", "true", "false", "false"]);
    await userEvent.click(screen.getByRole("button", { name: "2 hr" }));
    expect(state().duration).toBe(120);
    expect(pressed("2 hr")).toBe("true");
    expect(pressed("1½ hr")).toBe("false");
    expect(screen.queryByLabelText("Duration (minutes)")).not.toBeInTheDocument();
  });

  it("reveals the minutes field under Other and keeps what is typed", async () => {
    render(<Harness />);
    await userEvent.click(screen.getByRole("button", { name: "Other" }));
    const minutes = screen.getByLabelText("Duration (minutes)");
    expect(minutes).toHaveValue(90);
    expect(pressed("Other")).toBe("true");
    expect(pressed("1½ hr")).toBe("false");
    await userEvent.clear(minutes);
    await userEvent.type(minutes, "60");
    // Typing a preset's length stays on Other until a preset is picked.
    expect(state().duration).toBe(60);
    expect(pressed("Other")).toBe("true");
    await userEvent.click(screen.getByRole("button", { name: "1 hr" }));
    expect(screen.queryByLabelText("Duration (minutes)")).not.toBeInTheDocument();
  });

  it("opens on Other, with its minutes, when the length is not a preset", () => {
    render(<Harness init={{ duration: 45 }} />);
    expect(pressed("Other")).toBe("true");
    expect(screen.getByLabelText("Duration (minutes)")).toHaveValue(45);
  });

  it("shows the weekday chips under Every week and picks the date's weekday", async () => {
    render(<Harness />);
    expect(pressed("Just once")).toBe("true");
    expect(screen.queryByRole("group", { name: "Repeat on" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Every week" }));
    expect(screen.getByRole("group", { name: "Repeat on" })).toBeInTheDocument();
    expect(state()).toMatchObject({ recurring: true, days: ["WE"] });
    await userEvent.click(screen.getByRole("button", { name: "Fri" }));
    expect(state().days).toEqual(["WE", "FR"]);
    await userEvent.click(screen.getByRole("button", { name: "Just once" }));
    expect(screen.queryByRole("group", { name: "Repeat on" })).not.toBeInTheDocument();
  });

  it("offers Repeat until once the event repeats, and a raw rule when the chips can't say it", async () => {
    const { unmount } = render(<Harness until />);
    expect(screen.queryByLabelText(/^Repeat until/)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Every week" }));
    expect(screen.getByLabelText(/^Repeat until/)).toBeInTheDocument();
    unmount();
    render(<Harness customRule until init={{ recurring: true }} />);
    expect(screen.getByLabelText("Recurrence rule")).toHaveValue("FREQ=DAILY");
    expect(screen.queryByRole("button", { name: "Every week" })).not.toBeInTheDocument();
    expect(screen.getByLabelText(/^Repeat until/)).toBeInTheDocument();
  });

  it("names the zone on one line and opens the shared picker from Change", async () => {
    render(<Harness />);
    expect(screen.getByText("Mountain time")).toBeInTheDocument();
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Change time zone" }));
    const zone = screen.getByRole("combobox", { name: "Time zone" });
    expect(zone).toHaveValue("Denver (Mountain)");
    expect(zone).toHaveAccessibleDescription("Pick the zone.");
    expect(zone).toHaveFocus();
  });
});
