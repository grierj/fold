import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Messages } from "../../api/messages";
import type { AudienceSummary } from "../../types";
import { bookClub, bookNight, composeOptions, summary } from "../../test/compose";
import { emptyChoice, type AudienceChoice } from "../../lib/messages";
import { AudiencePicker } from "./AudiencePicker";

// The picker waits this long after a change before asking for the count.
const COUNT_DELAY_MS = 300;

vi.mock("../../api/messages", () => ({ Messages: { audiencePreview: vi.fn() } }));

function Harness({ start = emptyChoice(), everyone = false }: { start?: AudienceChoice; everyone?: boolean }) {
  const [c, setC] = useState(start);
  return <AudiencePicker id="aud" options={composeOptions({ canMessageEveryone: everyone })} choice={c} onChange={setC} />;
}

const LONG_GROUP = "Saint Brigid's Wednesday Evening Women's Bible Study and Prayer Fellowship (North Campus)";
const LONG_EVENT = "Saint Brigid's Wednesday Evening Women's Bible Study and Prayer Fellowship (North Campus) Potluck";
const HINT = "Your groups and the events you lead or host.";

function LongHarness({ target, everyone = false }: { target: string; everyone?: boolean }) {
  const [c, setC] = useState({ ...emptyChoice(), target });
  const options = composeOptions({
    canMessageEveryone: everyone,
    groups: [bookClub, { id: 9, name: LONG_GROUP }],
    events: [bookNight, { ...bookNight, id: 4, title: LONG_EVENT }],
  });
  return <AudiencePicker id="aud" options={options} choice={c} onChange={setC} />;
}

const optionText = (select: HTMLElement, value: string) => select.querySelector(`option[value="${value}"]`)?.textContent ?? "";

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

  it("shortens a long name in the middle, keeps its value, and shows the full name once chosen (#288)", () => {
    render(<LongHarness target="" />);
    const select = screen.getByRole("combobox", { name: "Send to" });
    const shown = optionText(select, "group:9");
    expect(shown).not.toBe(LONG_GROUP);
    expect(shown.startsWith("Saint Brigid's")).toBe(true);
    expect(shown.endsWith("(North Campus)")).toBe(true);
    expect(optionText(select, "group:7")).toBe("Book club");
    expect(screen.queryByText(LONG_GROUP)).toBeNull();
    expect(select).not.toHaveAttribute("title");

    fireEvent.change(select, { target: { value: "group:9" } });
    expect(select).toHaveValue("group:9");
    expect(select).toHaveAttribute("title", LONG_GROUP);
    expect(select).toHaveAccessibleDescription(`${HINT} ${LONG_GROUP}`);
    expect(screen.getByText(LONG_GROUP)).toBeVisible();
  });

  it("does the same for a long event title", () => {
    render(<LongHarness target="event:4" />);
    const select = screen.getByRole("combobox", { name: "Send to" });
    expect(optionText(select, "event:4").startsWith("Saint Brigid's")).toBe(true);
    expect(optionText(select, "event:4").endsWith("Potluck")).toBe(true);
    expect(select).toHaveAttribute("title", LONG_EVENT);
    expect(select).toHaveAccessibleDescription(`${HINT} ${LONG_EVENT}`);
    expect(screen.getByText(LONG_EVENT)).toBeVisible();
    expect(screen.getByRole("combobox", { name: "Which dates" })).toHaveAccessibleDescription("An event of Book club.");
  });

  it("tells apart long names that differ only in the middle, groups and events alike (#288)", () => {
    const wednesday = "Saint Brigid's Wednesday Evening Women's Bible Study (North Campus)";
    const thursday = wednesday.replace("Wednesday", "Thursday");
    const options = composeOptions({
      groups: [{ id: 21, name: wednesday }, { id: 22, name: thursday }, bookClub],
      events: [{ ...bookNight, id: 31, title: wednesday }, { ...bookNight, id: 32, title: thursday }],
    });
    function Alike() {
      const [c, setC] = useState(emptyChoice());
      return <AudiencePicker id="aud" options={options} choice={c} onChange={setC} />;
    }
    render(<Alike />);
    const select = screen.getByRole("combobox", { name: "Send to" });
    const groups = ["group:21", "group:22"].map((v) => optionText(select, v));
    const events = ["event:31", "event:32"].map((v) => optionText(select, v));
    expect(new Set(groups).size).toBe(2);
    expect(new Set(events).size).toBe(2);
    expect(groups[0]).toContain("Wednesday");
    expect(groups[1]).toContain("Thursday");
    expect(events[0]).toContain("Wednesday");
    expect(events[1]).toContain("Thursday");
    // A group and an event of the same name stay apart by their group heading.
    expect(optionText(select, "group:21")).toBe(optionText(select, "event:31"));
    expect(select.querySelector('optgroup[label="Groups"] option[value="group:21"]')).not.toBeNull();
    expect(select.querySelector('optgroup[label="Events"] option[value="event:31"]')).not.toBeNull();

    fireEvent.change(select, { target: { value: "event:32" } });
    expect(select).toHaveAttribute("title", thursday);
    expect(screen.getByText(thursday)).toBeVisible();
  });

  it("shows two groups with the very same long name alike, since nothing tells them apart", () => {
    const name = "Saint Brigid's Wednesday Evening Women's Bible Study (North Campus)";
    const options = composeOptions({ groups: [{ id: 21, name }, { id: 22, name }], events: [] });
    function Twins() {
      const [c, setC] = useState(emptyChoice());
      return <AudiencePicker id="aud" options={options} choice={c} onChange={setC} />;
    }
    render(<Twins />);
    const select = screen.getByRole("combobox", { name: "Send to" });
    const shown = optionText(select, "group:21");
    expect(shown).not.toBe(name);
    expect(optionText(select, "group:22")).toBe(shown);
    expect(shown.split("…")).toHaveLength(2);
  });

  it("shows nothing extra for a short name, Everyone, a name chosen earlier or no choice", () => {
    const { unmount } = render(<LongHarness target="group:7" />);
    let select = screen.getByRole("combobox", { name: "Send to" });
    expect(select).toHaveAttribute("title", "Book club");
    expect(select).toHaveAccessibleDescription(HINT);
    expect(screen.getAllByText("Book club")).toHaveLength(1);
    unmount();

    for (const [target, title] of [["all", "Everyone at church"], ["occurrence:99", "The date you chose earlier"], ["group:99", "The group you chose earlier"], ["event:99", "The event you chose earlier"]]) {
      const view = render(<LongHarness target={target} everyone />);
      select = screen.getByRole("combobox", { name: "Send to" });
      expect(select).toHaveAttribute("title", title);
      expect(select).toHaveAccessibleDescription(HINT);
      expect(document.getElementById("aud-full-name")).toBeNull();
      view.unmount();
    }

    render(<LongHarness target="" />);
    select = screen.getByRole("combobox", { name: "Send to" });
    expect(select).not.toHaveAttribute("title");
    expect(select).toHaveAccessibleDescription(HINT);
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
