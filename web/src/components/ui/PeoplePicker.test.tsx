import { act, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { People } from "../../api";
import type { PeopleSearchResponse, PersonSearchResult } from "../../types";
import { Field } from "./Field";
import { PeoplePicker } from "./PeoplePicker";

vi.mock("../../api", async (actual) => ({ ...(await actual<typeof import("../../api")>()), People: { search: vi.fn() } }));
const pat: PersonSearchResult = { selectionId: "opaque-pat", displayName: "Pat Lee", teen: false, sharedGroups: ["Choir"], otherGroups: ["Walking"] };
function Harness({ eligible = true }: { eligible?: boolean }) {
  const [value, setValue] = useState<PersonSearchResult | null>(null);
  return eligible ? <Field label="Find a person" hint="Search by name"><PeoplePicker value={value} onChange={setValue} /></Field> : null;
}
const box = () => screen.getByRole("combobox", { name: "Find a person" });
async function query(text: string) {
  fireEvent.focus(box());
  fireEvent.change(box(), { target: { value: text } });
  await act(() => vi.advanceTimersByTimeAsync(300));
}
function deferred() {
  let resolve!: (value: PeopleSearchResponse) => void;
  let reject!: (reason: Error) => void;
  const promise = new Promise<PeopleSearchResponse>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
beforeEach(() => { vi.useFakeTimers(); vi.mocked(People.search).mockReset().mockResolvedValue({ state: "results", people: [pat] }); });
afterEach(() => { vi.useRealTimers(); });

it("labels the combobox, hints without browsing, and debounces two Unicode code points", async () => {
  render(<Harness />);
  expect(box()).toHaveAccessibleDescription(/Search by name.*Type at least 2 letters/);
  await query("𐐀");
  expect(People.search).not.toHaveBeenCalled();
  fireEvent.change(box(), { target: { value: "𐐀a" } });
  await act(() => vi.advanceTimersByTimeAsync(299));
  expect(People.search).not.toHaveBeenCalled();
  fireEvent.change(box(), { target: { value: "𐐀ab" } });
  await act(() => vi.advanceTimersByTimeAsync(300));
  expect(People.search).toHaveBeenCalledExactlyOnceWith("𐐀ab");
  expect(screen.getByRole("status")).toHaveTextContent("1 person found");
});

it.each([
  ["short_query", "Type at least 2 letters"],
  ["throttled", "Too many searches. Try again in a minute."],
  ["no_results", "No one found. An admin can add people who aren't listed."],
] as const)("announces the calm %s state", async (state, copy) => {
  vi.mocked(People.search).mockResolvedValue({ state, people: [] });
  render(<Harness />); await query("pa");
  expect(screen.getByRole("status")).toHaveTextContent(copy);
  expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
});

it("shows safe group labels in order, adult images only, teen badge, and duplicate guidance", async () => {
  vi.mocked(People.search).mockResolvedValue({ state: "results", people: [pat, { ...pat, selectionId: "duplicate" }, { ...pat, selectionId: "teen", displayName: "Teen Pat", teen: true, avatarUrl: "/must-not-show" }, { ...pat, selectionId: "photo", displayName: "Photo Pat", avatarUrl: "/adult.jpg" }] });
  render(<Harness />); await query("pa");
  expect(screen.getByText("More than one match? Ask an admin.")).toBeInTheDocument();
  const options = screen.getAllByRole("option");
  expect(options[0].textContent).toBe("Pat LeeShared groups: ChoirOther groups: Walking");
  expect(options[0]).toHaveClass("min-h-11");
  expect(options[2]).toHaveTextContent("Teen");
  expect(options[2].querySelector("img")).toBeNull();
  expect(options[3].querySelector("img")).toHaveAttribute("src", "/adult.jpg");
  expect(screen.queryByText(/@/)).not.toBeInTheDocument();
});

it("uses arrows, active descendant, Escape and Enter without submitting the form", async () => {
  const submit = vi.fn();
  render(<form onSubmit={submit}><Harness /></form>); await query("pa");
  fireEvent.keyDown(box(), { key: "ArrowDown" });
  const option = screen.getByRole("option");
  expect(box()).toHaveAttribute("aria-activedescendant", option.id);
  expect(option).toHaveAttribute("aria-selected", "true");
  fireEvent.keyDown(box(), { key: "Escape" });
  expect(box()).toHaveAttribute("aria-expanded", "false");
  fireEvent.keyDown(box(), { key: "ArrowUp" });
  fireEvent.keyDown(box(), { key: "Enter" });
  expect(box()).toHaveValue("Pat Lee");
  expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  expect(submit).not.toHaveBeenCalled();
});

it("selects a teen for neutral guidance, not an unimplemented add action", async () => {
  vi.mocked(People.search).mockResolvedValue({ state: "results", people: [{ ...pat, teen: true }] });
  render(<Harness />); await query("pa"); fireEvent.click(screen.getByRole("option"));
  expect(screen.getByRole("status")).toHaveTextContent("Adding a teen needs their approval and an admin's approval. Ask your church office for help.");
  expect(screen.queryByRole("button")).not.toBeInTheDocument();
});

it.each([false, true])("ignores superseded responses and errors (reject=%s)", async (reject) => {
  const old = deferred(); vi.mocked(People.search).mockReturnValueOnce(old.promise);
  render(<Harness />); await query("old"); await query("new");
  await act(async () => { if (reject) old.reject(new Error("private text")); else old.resolve({ state: "no_results", people: [] }); });
  expect(screen.getByRole("option")).toHaveTextContent("Pat Lee");
  expect(screen.getByRole("status")).toHaveTextContent("1 person found");
});

it("invalidates on clear, selection and role-driven unmount", async () => {
  const old = deferred(); vi.mocked(People.search).mockReturnValueOnce(old.promise);
  const view = render(<Harness />); await query("old");
  fireEvent.change(box(), { target: { value: "" } });
  await act(async () => old.resolve({ state: "results", people: [pat] }));
  expect(screen.queryByRole("option")).not.toBeInTheDocument();
  await query("pa"); fireEvent.click(screen.getByRole("option"));
  await act(() => vi.advanceTimersByTimeAsync(300));
  expect(People.search).toHaveBeenCalledTimes(2);
  fireEvent.change(box(), { target: { value: "next" } });
  view.rerender(<Harness eligible={false} />);
  await act(() => vi.advanceTimersByTimeAsync(300));
  expect(People.search).toHaveBeenCalledTimes(2);
});

it("ignores pending errors after role-driven unmount", async () => {
  const old = deferred(); vi.mocked(People.search).mockReturnValueOnce(old.promise);
  const view = render(<Harness />); await query("old"); view.rerender(<Harness eligible={false} />);
  await act(async () => old.reject(new Error("private")));
  expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
});

it.each([false, true])("ignores a pending response/error after a newer result was selected (reject=%s)", async (reject) => {
  const old = deferred(); vi.mocked(People.search).mockReturnValueOnce(old.promise);
  render(<Harness />); await query("old"); await query("new");
  fireEvent.click(screen.getByRole("option"));
  await act(async () => { if (reject) old.reject(new Error("private")); else old.resolve({ state: "results", people: [{ ...pat, displayName: "Stale person" }] }); });
  expect(box()).toHaveValue("Pat Lee");
  expect(screen.getByRole("status")).toHaveTextContent("Pat Lee selected");
  expect(screen.queryByRole("option")).not.toBeInTheDocument();
});

it("ignores an error after clear", async () => {
  const old = deferred(); vi.mocked(People.search).mockReturnValueOnce(old.promise);
  render(<Harness />); await query("old"); fireEvent.change(box(), { target: { value: "" } });
  await act(async () => old.reject(new Error("private")));
  expect(screen.getByRole("status")).toHaveTextContent("Type at least 2 letters");
});
