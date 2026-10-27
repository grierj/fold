import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { ApiError, Groups } from "../api";
import type { DeparturePreview } from "../types";
import { GroupDepartureDialog } from "./GroupDepartureDialog";

vi.mock("../api", async (actual) => ({
  ...(await actual<typeof import("../api")>()),
  Groups: {
    departurePreview: vi.fn(),
    memberDeparturePreview: vi.fn(),
    confirmDeparture: vi.fn(),
    confirmMemberDeparture: vi.fn(),
  },
}));

const preview = (action: "leave" | "remove" = "leave", overrides: Partial<DeparturePreview> = {}): DeparturePreview => ({
  action,
  groupId: 7,
  groupName: "Tuesday Bible Study",
  subjectId: action === "leave" ? 2 : 3,
  subjectName: action === "leave" ? "Sam Member" : "Pat Person",
  affectedCount: 3,
  goingCount: 2,
  maybeCount: 1,
  waitlistedCount: 0,
  keepableCount: 3,
  defaultKeepCount: 2,
  snapshot: "snapshot-1",
  dates: [
    { occurrenceId: 11, date: "2026-12-01", title: "Underway night", startsAt: "2026-12-01T19:00:00Z", endsAt: "2026-12-01T20:00:00Z", status: "going", visibility: "private", defaultKeep: true, keepReason: "underway" },
    { occurrenceId: 12, date: "2026-12-02", title: "Soon night", startsAt: "2026-12-02T19:00:00Z", endsAt: "2026-12-02T20:00:00Z", status: "maybe", visibility: "invite_only", defaultKeep: true, keepReason: "cutoff" },
    { occurrenceId: 13, date: "2026-12-08", title: "Public night", startsAt: "2026-12-08T19:00:00Z", endsAt: "2026-12-08T20:00:00Z", status: "going", visibility: "public", defaultKeep: false, keepReason: "public" },
  ],
  ...overrides,
});

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(Groups.departurePreview).mockResolvedValue({ departure: preview() });
  vi.mocked(Groups.memberDeparturePreview).mockResolvedValue({ departure: preview("remove") });
  vi.mocked(Groups.confirmDeparture).mockResolvedValue({ departure: { action: "leave", groupId: 7, subjectId: 2, keptCount: 2, endedCount: 1 } });
  vi.mocked(Groups.confirmMemberDeparture).mockResolvedValue({ departure: { action: "remove", groupId: 7, subjectId: 3, keptCount: 2, endedCount: 1 } });
});

it("uses server safe defaults, exposes only date choices, and submits exact selected IDs", async () => {
  const completed = vi.fn();
  render(<GroupDepartureDialog groupId={7} onCancel={() => {}} onCompleted={completed} />);
  expect(await screen.findByRole("heading", { name: "Leave Tuesday Bible Study?" })).toBeInTheDocument();
  expect(screen.getByText(/3 upcoming dates, including 2 dates you said Going to/)).toBeInTheDocument();
  const underway = screen.getByRole("checkbox", { name: /Keep Underway night/ });
  const soon = screen.getByRole("checkbox", { name: /Keep Soon night/ });
  const publicDate = screen.getByRole("checkbox", { name: /Keep Public night/ });
  expect(underway).toBeChecked();
  expect(soon).toBeChecked();
  expect(publicDate).not.toBeChecked();
  expect(screen.queryByText(/attendee|location|series/i)).not.toBeInTheDocument();
  await userEvent.click(soon);
  await userEvent.click(publicDate);
  await userEvent.click(screen.getByRole("button", { name: "Leave group" }));
  expect(Groups.confirmDeparture).toHaveBeenCalledWith(7, "snapshot-1", [11, 13]);
  expect(completed).toHaveBeenCalledWith({ action: "leave", groupId: 7, subjectId: 2, keptCount: 2, endedCount: 1 });
});

it("still requires an explicit confirmation when there are zero affected dates", async () => {
  vi.mocked(Groups.departurePreview).mockResolvedValue({ departure: preview("leave", { affectedCount: 0, goingCount: 0, maybeCount: 0, keepableCount: 0, defaultKeepCount: 0, dates: [] }) });
  render(<GroupDepartureDialog groupId={7} onCancel={() => {}} onCompleted={() => {}} />);
  await screen.findByRole("heading", { name: "Leave Tuesday Bible Study?" });
  expect(screen.getByText("Your past attendance stays.")).toBeInTheDocument();
  expect(screen.queryByText(/0 upcoming dates?/i)).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Leave group" }));
  expect(Groups.confirmDeparture).toHaveBeenCalledWith(7, "snapshot-1", []);
});

it("omits the Going clause when affected dates have no Going answers", async () => {
  vi.mocked(Groups.departurePreview).mockResolvedValue({ departure: preview("leave", { affectedCount: 3, goingCount: 0, maybeCount: 3 }) });
  render(<GroupDepartureDialog groupId={7} onCancel={() => {}} onCompleted={() => {}} />);
  await screen.findByRole("heading", { name: "Leave Tuesday Bible Study?" });
  expect(screen.getByText("You'll be taken off 3 upcoming dates. Your past attendance stays.")).toBeInTheDocument();
  expect(screen.queryByText(/including 0/i)).not.toBeInTheDocument();
  expect(screen.getByText("2 dates will be kept. 1 date will end.")).toBeInTheDocument();
});

it("starts on the safe action, traps focus, cancels with Escape, and returns focus", async () => {
  const cancel = vi.fn();
  const trigger = document.createElement("button");
  trigger.textContent = "Leave group";
  document.body.append(trigger);
  trigger.focus();
  const view = render(<GroupDepartureDialog groupId={7} onCancel={cancel} onCompleted={() => {}} />);
  const dialog = await screen.findByRole("dialog");
  const stay = within(dialog).getByRole("button", { name: "Stay" });
  expect(stay).toHaveFocus();
  await userEvent.tab({ shift: true });
  expect(within(dialog).getByRole("checkbox", { name: /Keep Public night/ })).toHaveFocus();
  within(dialog).getByRole("button", { name: "Leave group" }).focus();
  await userEvent.tab();
  expect(within(dialog).getByRole("checkbox", { name: /Keep Underway night/ })).toHaveFocus();
  await userEvent.keyboard("{Escape}");
  expect(cancel).toHaveBeenCalledOnce();
  view.unmount();
  expect(trigger).toHaveFocus();
  trigger.remove();
});

it("uses the connected fallback instead of focusing a detached return target", async () => {
  const trigger = document.createElement("button");
  const fallback = document.createElement("button");
  document.body.append(trigger, fallback);
  trigger.focus();
  const view = render(<GroupDepartureDialog groupId={7} onCancel={() => {}} onCompleted={() => {}} fallbackFocus={() => fallback.focus()} />);
  await screen.findByRole("heading", { name: "Leave Tuesday Bible Study?" });
  trigger.remove();
  view.unmount();
  expect(fallback).toHaveFocus();
  fallback.remove();
});

it("refreshes a stale preview, resets to new server defaults, preserves the note, and requires another click", async () => {
  const fresh = preview("remove", {
    snapshot: "snapshot-2",
    affectedCount: 2,
    goingCount: 1,
    dates: [
      { occurrenceId: 21, date: "2026-12-03", title: "Changed soon night", startsAt: "2026-12-03T19:00:00Z", endsAt: "2026-12-03T20:00:00Z", status: "going", visibility: "private", defaultKeep: true, keepReason: "cutoff" },
      { occurrenceId: 22, date: "2026-12-10", title: "Changed public night", startsAt: "2026-12-10T19:00:00Z", endsAt: "2026-12-10T20:00:00Z", status: "maybe", visibility: "public", defaultKeep: false, keepReason: "public" },
    ],
    defaultKeepCount: 1,
    keepableCount: 2,
  });
  vi.mocked(Groups.memberDeparturePreview)
    .mockResolvedValueOnce({ departure: preview("remove") })
    .mockResolvedValueOnce({ departure: fresh });
  vi.mocked(Groups.confirmMemberDeparture)
    .mockRejectedValueOnce(new ApiError(409, "private", { code: "departure_stale" }))
    .mockResolvedValueOnce({ departure: { action: "remove", groupId: 7, subjectId: 3, keptCount: 1, endedCount: 1 } });
  render(<GroupDepartureDialog groupId={7} subjectId={3} allowNote onCancel={() => {}} onCompleted={() => {}} />);
  await userEvent.type(await screen.findByLabelText("Note for church admins (optional)"), "Pastoral follow-up");
  await userEvent.click(screen.getByRole("checkbox", { name: /Keep Public night/ }));
  await userEvent.click(screen.getByRole("button", { name: "Remove from group" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("refreshed the choices");
  expect(screen.getByLabelText("Note for church admins (optional)")).toHaveValue("Pastoral follow-up");
  expect(screen.getByRole("checkbox", { name: /Keep Changed soon night/ })).toBeChecked();
  expect(screen.getByRole("checkbox", { name: /Keep Changed public night/ })).not.toBeChecked();
  expect(Groups.confirmMemberDeparture).toHaveBeenCalledTimes(1);
  await userEvent.click(screen.getByRole("button", { name: "Remove from group" }));
  expect(Groups.confirmMemberDeparture).toHaveBeenLastCalledWith(7, 3, "snapshot-2", [21], "Pastoral follow-up");
});

it("keeps focus trapped when every control is disabled during a pending confirmation", async () => {
  let finish!: (value: { departure: { action: "leave"; groupId: number; subjectId: number; keptCount: number; endedCount: number } }) => void;
  vi.mocked(Groups.confirmDeparture).mockReturnValue(new Promise((resolve) => { finish = resolve; }));
  render(<GroupDepartureDialog groupId={7} onCancel={() => {}} onCompleted={() => {}} />);
  const confirm = await screen.findByRole("button", { name: "Leave group" });
  await userEvent.click(confirm);
  expect(confirm).toBeDisabled();
  expect(confirm).toHaveAttribute("aria-busy", "true");
  const dialog = screen.getByRole("dialog");
  await waitFor(() => expect(dialog).toHaveFocus());
  expect(dialog.querySelectorAll("button:not([disabled]), input:not([disabled]), textarea:not([disabled])")).toHaveLength(0);
  await userEvent.tab();
  expect(dialog).toHaveFocus();
  await userEvent.tab({ shift: true });
  expect(dialog).toHaveFocus();
  await userEvent.click(confirm);
  expect(Groups.confirmDeparture).toHaveBeenCalledTimes(1);
  finish({ departure: { action: "leave", groupId: 7, subjectId: 2, keptCount: 2, endedCount: 1 } });
  await waitFor(() => expect(confirm).toBeEnabled());
});

it("drops stale person and group names when authority is gone", async () => {
  vi.mocked(Groups.confirmMemberDeparture).mockRejectedValue(new ApiError(409, "private", { code: "departure_unavailable" }));
  vi.mocked(Groups.memberDeparturePreview)
    .mockResolvedValueOnce({ departure: preview("remove") })
    .mockRejectedValueOnce(new ApiError(409, "private", { code: "departure_unavailable" }));
  const unavailable = vi.fn();
  render(<GroupDepartureDialog groupId={7} subjectId={3} onCancel={() => {}} onCompleted={() => {}} onUnavailable={unavailable} />);
  await userEvent.click(await screen.findByRole("button", { name: "Remove from group" }));
  await waitFor(() => expect(screen.getByRole("heading", { name: "Departure unavailable" })).toBeInTheDocument());
  expect(screen.queryByText(/Pat Person|Tuesday Bible Study/)).not.toBeInTheDocument();
  expect(screen.getByRole("alert")).toHaveTextContent("isn't available anymore");
  expect(unavailable).toHaveBeenCalled();
  expect(Groups.confirmMemberDeparture).toHaveBeenCalledTimes(1);
});
