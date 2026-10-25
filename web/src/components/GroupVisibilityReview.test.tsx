import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { GroupVisibilityReview } from "./GroupVisibilityReview";

describe("GroupVisibilityReview", () => {
  const impact = {
    visibility: "invite_only" as const,
    tightened: [{ id: 1, title: "Soup night", visibility: "private" as const, registrants: 2 }],
    leaderOpened: [{ id: 2, title: "Bake sale", visibility: "public" as const, registrants: 1 }],
  };

  it("lists the events and waits for a choice on each opened one", () => {
    const onConfirm = vi.fn();
    render(<GroupVisibilityReview impact={impact} busy={false} onConfirm={onConfirm} onCancel={() => {}} />);
    expect(screen.getByText(/2 people registered will be cancelled and notified/)).toBeInTheDocument();
    const confirm = screen.getByRole("button", { name: "Make the group invite-only" });
    expect(confirm).toBeDisabled();
    fireEvent.click(screen.getByRole("radio", { name: /Make it invite-only/ }));
    expect(confirm).toBeEnabled();
    fireEvent.click(confirm);
    expect(onConfirm).toHaveBeenCalledWith({ "2": "tighten" });
  });

  it("needs no choice when only inherited events change", () => {
    const onConfirm = vi.fn();
    render(
      <GroupVisibilityReview impact={{ ...impact, leaderOpened: [] }} busy={false} onConfirm={onConfirm} onCancel={() => {}} />
    );
    fireEvent.click(screen.getByRole("button", { name: "Make the group invite-only" }));
    expect(onConfirm).toHaveBeenCalledWith({});
  });
});

it("keys date decisions separately from their series and formats the date", () => {
  const onConfirm = vi.fn();
  render(<GroupVisibilityReview impact={{ visibility: "invite_only", tightened: [], leaderOpened: [{ id: 7, title: "Welcome evening", visibility: "public", registrants: 0, date: "2026-12-01", occurrenceId: 51, decisionKey: "date:51" }] }} busy={false} onConfirm={onConfirm} onCancel={() => {}} />);
  expect(screen.getByText(/Tuesday, December 1/)).toBeInTheDocument();
  fireEvent.click(screen.getByRole("radio", { name: /^Keep it public/ }));
  fireEvent.click(screen.getByRole("button", { name: "Make the group invite-only" }));
  expect(onConfirm).toHaveBeenCalledWith({ "date:51": "keep" });
});
