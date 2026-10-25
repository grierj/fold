import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { Events } from "../api";
import { occurrence } from "../test/occurrence";
import { DateEditor } from "./DateEditor";

vi.mock("../api", () => ({ Events: { updateOccurrence: vi.fn(), dateVisibilityImpact: vi.fn() } }));
beforeEach(() => { vi.resetAllMocks(); vi.mocked(Events.dateVisibilityImpact).mockResolvedValue({ visibility: "public", tightened: [], leaderOpened: [] }); vi.mocked(Events.updateOccurrence).mockResolvedValue({ occurrence: occurrence({}) }); });

it("opens one date through the shared visibility field without changing the series", async () => {
  const onSaved = vi.fn();
  render(<DateEditor instance={occurrence({ visibility: "private", seriesVisibility: "private", groupVisibility: "invite_only", groupName: "Hidden fellowship" })} onSaved={onSaved} onClose={() => {}} />);
  expect(screen.getByRole("radio", { name: "Same as series (private)" })).toBeChecked();
  await userEvent.click(screen.getByRole("radio", { name: /^Public/ }));
  expect(screen.getByText(/Only leaders will see who signed up/)).toBeInTheDocument();
  await userEvent.type(screen.getByRole("textbox", { name: /^Reason/ }), "Welcome neighbours");
  await userEvent.click(screen.getByRole("button", { name: "Save this date" }));
  expect(Events.updateOccurrence).toHaveBeenCalledWith(7, "2026-12-01", { visibility: "public", reason: "Welcome neighbours", confirm: false });
  expect(onSaved).toHaveBeenCalled();
});

it("clears an explicit date value only after acknowledging its cancellation count", async () => {
  vi.mocked(Events.dateVisibilityImpact).mockResolvedValue({ visibility: "private", tightened: [{ id: 7, title: "One date", visibility: "public", registrants: 2 }], leaderOpened: [] });
  vi.spyOn(window, "confirm").mockReturnValue(false);
  render(<DateEditor instance={occurrence({ visibility: "public", visibilityOverride: "public", seriesVisibility: "private" })} onSaved={() => {}} onClose={() => {}} />);
  await userEvent.click(screen.getByRole("radio", { name: "Same as series (private)" }));
  await userEvent.click(screen.getByRole("button", { name: "Save this date" }));
  expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining("2 registered people"));
  expect(Events.updateOccurrence).not.toHaveBeenCalled();
});
