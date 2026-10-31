import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Messages } from "../../messages-api";
import type { AudienceSummary } from "../../types";
import { composeOptions, summary } from "../../test/compose";
import { emptyChoice, type AudienceChoice } from "../../lib/messages";
import { AudiencePicker, COUNT_DELAY_MS } from "./AudiencePicker";

vi.mock("../../messages-api", () => ({ Messages: { audiencePreview: vi.fn() } }));

function Harness({ start = emptyChoice(), everyone = false }: { start?: AudienceChoice; everyone?: boolean }) {
  const [c, setC] = useState(start);
  return <AudiencePicker id="aud" options={composeOptions({ canMessageEveryone: everyone })} choice={c} onChange={setC} />;
}

// Each call answers when its resolver runs, so a test picks the order.
function pending() {
  const calls: ((s: AudienceSummary) => void)[] = [];
  vi.mocked(Messages.audiencePreview).mockImplementation(() => new Promise((resolve) => calls.push((s) => resolve({ summary: s }))));
  return calls;
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => { vi.useRealTimers(); vi.mocked(Messages.audiencePreview).mockReset(); });

const wait = () => act(async () => { vi.advanceTimersByTime(COUNT_DELAY_MS); });

describe("AudiencePicker", () => {
  it("offers only the author's groups and events, and Everyone only to admins", () => {
    const { unmount } = render(<Harness />);
    const select = screen.getByRole("combobox", { name: "Send to" });
    expect(within(select).getAllByRole("option").map((o) => o.textContent)).toEqual(["Choose a group or event", "Book club", "Book night"]);
    unmount();
    render(<Harness everyone />);
    expect(within(screen.getByRole("combobox", { name: "Send to" })).getByRole("option", { name: "Everyone at church" })).toBeInTheDocument();
  });

  it("shows status chips for an event and role chips for a group", async () => {
    pending();
    render(<Harness />);
    fireEvent.change(screen.getByRole("combobox", { name: "Send to" }), { target: { value: "event:3" } });
    expect(screen.getByRole("button", { name: "Going" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "Waitlisted" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "Maybe" })).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByRole("combobox", { name: "Which dates" })).toHaveValue("all");
    expect(screen.getByRole("combobox", { name: "Which dates" })).toHaveAccessibleDescription("An event of Book club.");
    fireEvent.change(screen.getByRole("combobox", { name: "Send to" }), { target: { value: "group:7" } });
    expect(screen.getByRole("button", { name: "Members" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "Leaders" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.queryByRole("combobox", { name: "Which dates" })).toBeNull();
  });

  it("counts after a pause and drops a count that comes back after a newer change", async () => {
    const calls = pending();
    render(<Harness start={{ ...emptyChoice(), target: "group:7" }} />);
    await wait();
    expect(Messages.audiencePreview).toHaveBeenLastCalledWith({ type: "group", groupId: 7, roles: ["member", "leader"] });
    fireEvent.click(screen.getByRole("button", { name: "Leaders" }));
    await wait();
    expect(Messages.audiencePreview).toHaveBeenLastCalledWith({ type: "group", groupId: 7, roles: ["member"] });
    expect(calls).toHaveLength(2);
    // The newer answer lands first; the older one must not replace it.
    await act(async () => { calls[1](summary({ people: 2, eligible: 2, teens: 0, skipped: {} })); });
    await act(async () => { calls[0](summary({ people: 142, eligible: 133, skipped: { "turned off messages from leaders": 6, "no email address": 3 } })); });
    expect(screen.getByRole("status")).toHaveTextContent("2 people; everyone on the list will get it.");
    expect(screen.getByRole("status")).not.toHaveTextContent("142");
  });

  it("says it shows only the next 10 dates when there are that many", () => {
    const many = Array.from({ length: 10 }, (_, i) => ({ occurrenceId: 100 + i, date: `2026-12-${10 + i}`, startsAt: `2026-12-${10 + i}T02:00:00Z` }));
    const o = composeOptions();
    render(<AudiencePicker id="aud" options={{ ...o, events: [{ ...o.events[0], dates: many }] }} choice={{ ...emptyChoice(), target: "event:3" }} onChange={() => {}} />);
    expect(screen.getByRole("combobox", { name: "Which dates" })).toHaveAccessibleDescription("An event of Book club. Showing the next 10 dates.");
  });

  it("keeps the live region busy while counting and announces only the result", async () => {
    const calls = pending();
    render(<Harness start={{ ...emptyChoice(), target: "group:7" }} />);
    const status = screen.getByRole("status");
    expect(status).toHaveAttribute("aria-busy", "true");
    expect(status).not.toHaveTextContent("Counting");
    expect(screen.getByText("Counting who gets it…")).toBeInTheDocument();
    await wait();
    await act(async () => { calls[0](summary()); });
    expect(status).toHaveAttribute("aria-busy", "false");
    expect(screen.queryByText("Counting who gets it…")).toBeNull();
  });

  it("waits for the pause before counting at all", async () => {
    pending();
    render(<Harness start={{ ...emptyChoice(), target: "group:7" }} />);
    fireEvent.click(screen.getByRole("button", { name: "Leaders" }));
    fireEvent.click(screen.getByRole("button", { name: "Leaders" }));
    await wait();
    expect(Messages.audiencePreview).toHaveBeenCalledTimes(1);
  });

  it("lists recipients with Teen badges and who won't get it, never an address", async () => {
    const calls = pending();
    render(<Harness start={{ ...emptyChoice(), target: "group:7" }} />);
    await wait();
    await act(async () => { calls[0](summary()); });
    expect(screen.getByRole("status")).toHaveTextContent("3 people; 1 won't get it: 1 turned off messages from leaders");
    expect(screen.getByRole("status")).toHaveTextContent("1 of them is a teen.");
    const items = screen.getAllByRole("listitem");
    expect(items[1]).toHaveTextContent("Opal Opted· Won't get this");
    expect(within(items[2]).getByText("Teen")).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/@/);
  });

  it("asks for at least one kind of person", () => {
    render(<Harness start={{ ...emptyChoice(), target: "group:7", roles: ["member"] }} />);
    fireEvent.click(screen.getByRole("button", { name: "Members" }));
    expect(screen.getByText("Choose at least one.")).toBeInTheDocument();
  });
});
