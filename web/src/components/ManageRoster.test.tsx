import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { ApiError, Events } from "../api";
import { occurrence } from "../test/occurrence";
import type { Attendee } from "../types";
import { ManageRoster } from "./ManageRoster";

vi.mock("../api", async (actual) => ({ ...(await actual<typeof import("../api")>()), Events: { removeRegistration: vi.fn(), promoteRegistration: vi.fn(), restoreRegistration: vi.fn(), addRegistration: vi.fn() } }));
const child: Attendee = { registrationId: 91, name: "Kit", status: "waitlisted", guestCount: 0, attendanceMode: "remote", householdPerson: { personId: 3, name: "Kit", kind: "child", age: 6, householdName: "Leader household", registeredBy: { userId: 7, name: "Holder" } } };
beforeEach(() => { vi.resetAllMocks(); vi.spyOn(window, "confirm").mockReturnValue(true); });

it("acts on the registration ID when user and person IDs overlap", async () => {
  const onChanged = vi.fn();
  render(<ManageRoster instance={occurrence({ attendees: [child, { registrationId: 92, userId: 3, name: "Sam", status: "going", attendanceMode: "in_person", guestCount: 0 }] })} onChanged={onChanged} onClose={() => {}} />);
  await userEvent.click(screen.getByRole("button", { name: "Promote" }));
  expect(Events.promoteRegistration).toHaveBeenCalledWith(7, "2026-12-01", 91);
  await userEvent.click(screen.getByRole("button", { name: "Remove Kit" }));
  expect(Events.removeRegistration).toHaveBeenCalledWith(7, "2026-12-01", 91);
  expect(screen.getByRole("button", { name: "Remove Sam" })).toHaveClass("btn-danger-outline");
});

it("recovers the named person with the original holder and confirms their own pool", async () => {
  render(<ManageRoster instance={occurrence({ remoteAvailable: true, remoteCapacity: 1, remoteSpotsLeft: 0, spotsLeft: 4, removedRegistrations: [{ ...child, status: "removed" }] })} onChanged={() => {}} onClose={() => {}} />);
  await userEvent.click(screen.getByRole("button", { name: "Add back Kit" }));
  expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining("remote seats are full"));
  expect(Events.restoreRegistration).toHaveBeenCalledWith(7, "2026-12-01", { personId: 3, registeredBy: 7 }, "remote", true);
});

it("shows childcare refusal without pretending the restore succeeded", async () => {
  vi.mocked(Events.restoreRegistration).mockRejectedValue(new ApiError(409, "private server wording", { code: "childcare_full" }));
  const changed = vi.fn();
  render(<ManageRoster instance={occurrence({ removedRegistrations: [{ ...child, status: "not_restored" }] })} onChanged={changed} onClose={() => {}} />);
  await userEvent.click(screen.getByRole("button", { name: "Add back Kit" }));
  expect(screen.getByRole("alert")).toHaveTextContent("Childcare is full"); expect(changed).not.toHaveBeenCalled();
});

it("stacks actions under details and names statuses in words (PR 110 M1, N2)", () => {
  render(<ManageRoster instance={occurrence({ attendees: [{ ...child, attendanceMode: "in_person" }, { ...child, registrationId: 93, name: "Mary", attendanceMode: "in_person" }, { registrationId: 92, userId: 3, name: "Sam", status: "going", attendanceMode: "in_person", guestCount: 0 }] })} onChanged={() => {}} onClose={() => {}} />);
  expect(screen.getByText("Waitlist position 1")).toBeInTheDocument();
  expect(screen.getByText("Waitlist position 2")).toBeInTheDocument();
  expect(screen.getByText("Going")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Remove Sam" }).closest("li")).toHaveClass("flex-col", "sm:flex-row");
});

it("keeps waitlist positions per pool and places status before care notes", () => {
  render(<ManageRoster instance={occurrence({ childcareAvailable: true, attendees: [
    { ...child, registrationId: 101, attendanceMode: "remote", householdPerson: { ...child.householdPerson!, careNotes: "First note" } },
    { ...child, registrationId: 102, attendanceMode: "in_person" },
    { ...child, registrationId: 103, attendanceMode: "remote" },
  ] })} onChanged={() => {}} onClose={() => {}} />);
  expect(screen.getAllByText("Waitlist position 1")).toHaveLength(2);
  expect(screen.getByText("Waitlist position 2")).toBeInTheDocument();
  const note = screen.getByText("Care notes: First note");
  const row = note.closest("li");
  if (!row) throw new Error("missing roster row");
  const signer = within(row).getByText(/Signed up by/);
  expect(signer.nextElementSibling).toHaveTextContent("Waitlist position 1");
  expect(row.textContent?.indexOf("Waitlist position 1")).toBeLessThan(row.textContent?.indexOf("Care notes") ?? 0);
});
