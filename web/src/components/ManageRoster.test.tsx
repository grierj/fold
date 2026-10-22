import { render, screen } from "@testing-library/react";
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
