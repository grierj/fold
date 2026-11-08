import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ErasureReasonItem } from "../types";
import { CHANGED_COPY, ReasonReviewList, type ReasonChoice } from "./ReasonReviewList";

const item = (over: Partial<ErasureReasonItem> = {}): ErasureReasonItem => ({
  requestId: "1", reviewHash: "a".repeat(64), role: "subject", purpose: "contact", status: "pending",
  createdAt: "2026-11-01T12:00:00Z", counterpartName: "Lee Leader", counterpartDeleted: false,
  reviewerIsParty: false, reason: "Please call me about the ride.", ...over,
});

function show(items: ErasureReasonItem[], choices: Record<string, ReasonChoice | undefined> = {}, changed?: Set<string>) {
  const onChoose = vi.fn();
  render(<ReasonReviewList items={items} choices={choices} changed={changed} onChoose={onChoose} />);
  return onChoose;
}

afterEach(cleanup);

describe("ReasonReviewList", () => {
  it("shows the reason text and a Keep text / Blank text pair per item, with nothing chosen", () => {
    show([item({ requestId: "1" }), item({ requestId: "2", reason: "Second reason" })]);
    expect(screen.getByText("Please call me about the ride.")).toBeInTheDocument();
    expect(screen.getByText("Second reason")).toBeInTheDocument();
    expect(screen.getAllByRole("radio")).toHaveLength(4);
    for (const r of screen.getAllByRole("radio")) expect(r).not.toBeChecked();
    expect(screen.getAllByRole("radio", { name: "Keep text" })).toHaveLength(2);
    expect(screen.getAllByRole("radio", { name: "Blank text" })).toHaveLength(2);
  });

  it("has no bulk control: no choose-all, keep-all or blank-all of any kind", () => {
    show([item({ requestId: "1" }), item({ requestId: "2" })]);
    expect(screen.queryAllByRole("button")).toHaveLength(0);
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(screen.queryByText(/\b(all|every)\b/i)).not.toBeInTheDocument();
  });

  it("groups each item's pair under its own legend, and the pairs do not share a name", () => {
    show([item({ requestId: "1" }), item({ requestId: "2" })]);
    const first = screen.getByRole("group", { name: "Reason 1 of 2" });
    const second = screen.getByRole("group", { name: "Reason 2 of 2" });
    const names = [...within(first).getAllByRole("radio"), ...within(second).getAllByRole("radio")].map((r) => r.getAttribute("name"));
    expect(new Set(names).size).toBe(2);
  });

  it("reports the item and the choice, and shows only the choice it was given", async () => {
    const a = item({ requestId: "1" });
    const b = item({ requestId: "2" });
    const onChoose = show([a, b], { "1": "remove", "2": "keep" });
    const first = within(screen.getByRole("group", { name: "Reason 1 of 2" }));
    const second = within(screen.getByRole("group", { name: "Reason 2 of 2" }));
    expect(first.getByRole("radio", { name: "Blank text" })).toBeChecked();
    expect(first.getByRole("radio", { name: "Keep text" })).not.toBeChecked();
    expect(second.getByRole("radio", { name: "Keep text" })).toBeChecked();
    await userEvent.click(first.getByRole("radio", { name: "Keep text" }));
    expect(onChoose).toHaveBeenCalledWith(a, "keep");
    await userEvent.click(second.getByRole("radio", { name: "Blank text" }));
    expect(onChoose).toHaveBeenCalledWith(b, "remove");
  });

  it("shows the occurrence_add note on occurrence_add items only", () => {
    show([
      item({ requestId: "1", purpose: "occurrence_add", reason: "Add me" }),
      item({ requestId: "2", purpose: "group_add" }),
      item({ requestId: "3", purpose: "contact" }),
    ]);
    expect(screen.getAllByText("This reason wasn't shown to the people who decided this request.")).toHaveLength(1);
    const noted = screen.getByText("Add me").closest("li")!;
    expect(within(noted).getByText("This reason wasn't shown to the people who decided this request.")).toBeInTheDocument();
  });

  it("shows the party badge, as text, only on items the reviewer is part of", () => {
    show([item({ requestId: "1", reviewerIsParty: true, reason: "Mine" }), item({ requestId: "2", reviewerIsParty: false })]);
    expect(screen.getAllByText("You're part of this request")).toHaveLength(1);
    expect(within(screen.getByText("Mine").closest("li")!).getByText("You're part of this request")).toBeInTheDocument();
  });

  it("never names a deleted counterpart and tells two of them apart by reference", () => {
    show([
      item({ requestId: "1", counterpartDeleted: true, counterpartName: "Deleted account", counterpartRef: "41", role: "requester" }),
      item({ requestId: "2", counterpartDeleted: true, counterpartName: "Deleted account", counterpartRef: "52", role: "subject" }),
    ]);
    expect(screen.getByText(/Deleted account · ref 41/)).toBeInTheDocument();
    expect(screen.getByText(/Deleted account · ref 52/)).toBeInTheDocument();
    expect(screen.queryByText(/Lee Leader/)).not.toBeInTheDocument();
  });

  it("names a living counterpart and says which part the account had", () => {
    show([item({ requestId: "1", role: "requester", counterpartName: "Pat Person" }), item({ requestId: "2", role: "subject", counterpartName: "Lee Leader" })]);
    expect(screen.getByText(/They made this request about Pat Person\./)).toBeInTheDocument();
    expect(screen.getByText(/Lee Leader made this request about them\./)).toBeInTheDocument();
  });

  it("says what kind of request, its status and the context label when there is one", () => {
    show([item({ purpose: "group_add", status: "granted", contextLabel: "Choir" })]);
    expect(screen.getByText("Request to add to a group")).toBeInTheDocument();
    expect(screen.getByText(/Approved\./)).toBeInTheDocument();
    expect(screen.getByText(/Choir\./)).toBeInTheDocument();
  });

  it("keeps the reason's line breaks and text exactly, without treating it as markup", () => {
    show([item({ reason: "Line one\n<b>not bold</b>" })]);
    const text = screen.getByText(/Line one/);
    expect(text.textContent).toBe("Line one\n<b>not bold</b>");
    expect(text.querySelector("b")).toBeNull();
  });

  it("marks a changed item in words until it is chosen", () => {
    const { rerender } = render(
      <ReasonReviewList items={[item({ requestId: "1" }), item({ requestId: "2" })]} choices={{}} changed={new Set(["1"])} onChoose={vi.fn()} />,
    );
    expect(screen.getAllByText(CHANGED_COPY)).toHaveLength(1);
    expect(screen.getByText(CHANGED_COPY).closest("li")).toHaveAttribute("data-changed", "true");
    rerender(<ReasonReviewList items={[item({ requestId: "1" }), item({ requestId: "2" })]} choices={{ "1": "keep" }} changed={new Set(["1"])} onChoose={vi.fn()} />);
    expect(screen.queryByText(CHANGED_COPY)).not.toBeInTheDocument();
  });

  it("turns every radio off when disabled", () => {
    render(<ReasonReviewList items={[item()]} choices={{}} onChoose={vi.fn()} disabled />);
    for (const r of screen.getAllByRole("radio")) expect(r).toBeDisabled();
  });
});
