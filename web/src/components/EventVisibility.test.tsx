import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { EventVisibilityControl } from "./EventVisibility";

const { setVisibility, visibilityImpact } = vi.hoisted(() => ({ setVisibility: vi.fn(), visibilityImpact: vi.fn() }));
vi.mock("../api", () => ({ ApiError: class extends Error {}, Events: { setVisibility, visibilityImpact } }));

// Making one event more private previews and confirms, like a group (DEC-48).
describe("event tightening", () => {
  it("shows the registrations it cancels and saves only after confirm", async () => {
    visibilityImpact.mockResolvedValue({
      visibility: "invite_only",
      tightened: [{ id: 7, title: "Picnic", visibility: "public", registrants: 2 }],
      leaderOpened: [],
    });
    setVisibility.mockResolvedValue({});
    const onSaved = vi.fn();
    render(<EventVisibilityControl eventId={7} groupName="Garden" groupVisibility="public" visibility="public" onSaved={onSaved} />);
    await userEvent.click(screen.getByRole("radio", { name: /^Invite only/ }));
    await userEvent.click(screen.getByRole("button", { name: "Save visibility" }));
    expect(await screen.findByText("Making this event invite-only")).toBeInTheDocument();
    expect(screen.getByRole("dialog")).toHaveTextContent("2 people registered will be cancelled and notified");
    expect(setVisibility).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Make this event invite-only" }));
    expect(setVisibility).toHaveBeenCalledWith(7, { visibility: "invite_only", reason: undefined, confirm: true });
    expect(onSaved).toHaveBeenCalled();
  });

  it("opening an event up saves without a preview", async () => {
    setVisibility.mockResolvedValue({});
    visibilityImpact.mockClear();
    render(<EventVisibilityControl eventId={8} groupName="Garden" groupVisibility="private" visibility="private" onSaved={() => {}} />);
    await userEvent.click(screen.getByRole("radio", { name: /^Public/ }));
    await userEvent.click(screen.getByRole("button", { name: "Save visibility" }));
    expect(visibilityImpact).not.toHaveBeenCalled();
    expect(setVisibility).toHaveBeenCalledWith(8, { visibility: "public", reason: undefined, confirm: false });
  });
});
