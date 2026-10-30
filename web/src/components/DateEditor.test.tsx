import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { DateRooms, Events, Site } from "../api";
import { occurrence } from "../test/occurrence";
import { DateEditor } from "./DateEditor";

vi.mock("../api", () => ({ ApiError: class extends Error {}, Events: { updateOccurrence: vi.fn(), dateVisibilityImpact: vi.fn() }, Site: { get: vi.fn() }, DateRooms: { list: vi.fn(), set: vi.fn() } }));
beforeEach(() => { vi.resetAllMocks(); vi.mocked(Site.get).mockResolvedValue({ name: "Fold", timezone: "UTC", selfSignup: "off", roomsEnabled: false }); vi.mocked(Events.dateVisibilityImpact).mockResolvedValue({ visibility: "public", tightened: [], leaderOpened: [] }); vi.mocked(Events.updateOccurrence).mockResolvedValue({ occurrence: occurrence({}) }); });

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

it("puts the Room control beside Location when rooms are on, and not otherwise", async () => {
  vi.mocked(Site.get).mockResolvedValue({ name: "Fold", timezone: "UTC", selfSignup: "off", roomsEnabled: true });
  vi.mocked(DateRooms.list).mockResolvedValue({ rooms: [], booking: null });
  render(<DateEditor instance={occurrence({})} onSaved={() => {}} onClose={() => {}} />);
  expect(await screen.findByRole("combobox", { name: /^Room/ })).toBeInTheDocument();
  expect(DateRooms.list).toHaveBeenCalledWith(7, "2026-12-01");
  expect(screen.getByRole("textbox", { name: /^Location/ })).toBeInTheDocument();
});

it("leaves the Room control out while rooms are off", async () => {
  render(<DateEditor instance={occurrence({})} onSaved={() => {}} onClose={() => {}} />);
  await screen.findByRole("button", { name: "Save this date" });
  await waitFor(() => expect(Site.get).toHaveBeenCalled());
  expect(screen.queryByRole("combobox", { name: /^Room/ })).not.toBeInTheDocument();
  expect(DateRooms.list).not.toHaveBeenCalled();
});

it("keeps the rest of the editor working while the rooms fail to load", async () => {
  vi.mocked(Site.get).mockResolvedValue({ name: "Fold", timezone: "UTC", selfSignup: "off", roomsEnabled: true });
  vi.mocked(DateRooms.list).mockRejectedValue(new Error("down"));
  const onSaved = vi.fn();
  render(<DateEditor instance={occurrence({})} onSaved={onSaved} onClose={() => {}} />);
  expect(await screen.findByRole("button", { name: "Try again" })).toBeInTheDocument();
  await userEvent.type(screen.getByRole("textbox", { name: "Title" }), "!");
  await userEvent.click(screen.getByRole("button", { name: "Save this date" }));
  expect(Events.updateOccurrence).toHaveBeenCalledWith(7, "2026-12-01", { title: "Supper!" });
  expect(onSaved).toHaveBeenCalled();
});
