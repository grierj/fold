import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { ApiError, Events, People } from "../api";
import { occurrence } from "../test/occurrence";
import type { Attendee, User } from "../types";
import { ManageRoster } from "./ManageRoster";

const actor = vi.hoisted(() => ({ user: null as Pick<User, "id" | "status" | "accountType"> | null }));
vi.mock("../auth", () => ({ useAuth: () => actor }));
vi.mock("../api", async (actual) => ({ ...(await actual<typeof import("../api")>()), People: { search: vi.fn() }, Events: { removeRegistration: vi.fn(), promoteRegistration: vi.fn(), restoreRegistration: vi.fn(), addRegistration: vi.fn() } }));
const child: Attendee = { registrationId: 91, name: "Kit", status: "waitlisted", guestCount: 0, attendanceMode: "remote", householdPerson: { personId: 3, name: "Kit", kind: "child", age: 6, householdName: "Leader household", registeredBy: { userId: 7, name: "Holder" } } };
beforeEach(() => { vi.resetAllMocks(); actor.user = null; vi.spyOn(window, "confirm").mockReturnValue(true); });

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

it.each([null, { id: 10, status: "active", accountType: "teen" }, { id: 10, status: "disabled", accountType: "adult" }] as const)("offers no request to an ineligible actor %j", (user) => {
  actor.user = user;
  render(<ManageRoster instance={occurrence({ attendees: [{ registrationId: 92, userId: 3, name: "Hidden Pat", status: "going", attendanceMode: "in_person", guestCount: 0 }] })} onChanged={() => {}} onClose={() => {}} />);
  expect(screen.queryByRole("button", { name: "Request direct contact" })).not.toBeInTheDocument();
});

it("offers contact compose only for another real hidden account, not household people, missing IDs, self or visible contacts", () => {
  actor.user = { id: 10, status: "active", accountType: "adult" };
  const base: Attendee = { registrationId: 92, userId: 3, name: "Hidden Pat", status: "going", attendanceMode: "in_person", guestCount: 0 };
  render(<ManageRoster instance={occurrence({ attendees: [base, { ...base, registrationId: 93, userId: 10, name: "Self" }, { ...base, registrationId: 94, userId: 4, name: "Visible Sam", email: "sam@example.com", phone: "" }, { ...base, registrationId: 95, userId: undefined, name: "No account ID" }, { ...child, status: "going" }] })} onChanged={() => {}} onClose={() => {}} />);
  expect(screen.getAllByRole("button", { name: "Request direct contact" })).toHaveLength(1);
  expect(within(screen.getByText("Hidden Pat").closest("li")!).getByRole("button", { name: "Request direct contact" })).toBeInTheDocument();
});

it("adds an opaque adult selection and retains email as an alternative", async () => {
  actor.user = { id: 10, status: "active", accountType: "adult" };
  vi.mocked(People.search).mockResolvedValue({ state: "results", people: [{ selectionId: "opaque", displayName: "Pat", teen: false, sharedGroups: [], otherGroups: [] }] });
  const changed = vi.fn();
  render(<ManageRoster instance={occurrence({})} onChanged={changed} onClose={() => {}} />);
  await userEvent.type(screen.getByRole("combobox"), "Pat");
  await userEvent.click(await screen.findByRole("option"));
  await userEvent.click(screen.getByRole("button", { name: "Add" }));
  expect(Events.addRegistration).toHaveBeenCalledWith(7, "2026-12-01", { selectionId: "opaque" }, undefined);
  await userEvent.type(screen.getByLabelText("Add someone"), "known@example.com");
  await userEvent.click(screen.getByRole("button", { name: "Add" }));
  expect(Events.addRegistration).toHaveBeenLastCalledWith(7, "2026-12-01", "known@example.com", undefined);
  expect(changed).toHaveBeenCalledTimes(2);
});

it("shows teen approval guidance without an add, and no search after role loss", async () => {
  actor.user = { id: 10, status: "active", accountType: "adult" };
  vi.mocked(People.search).mockResolvedValue({ state: "results", people: [{ selectionId: "teen", displayName: "Teen Pat", teen: true, sharedGroups: [], otherGroups: [] }] });
  const view = render(<ManageRoster instance={occurrence({})} onChanged={() => {}} onClose={() => {}} />);
  await userEvent.type(screen.getByRole("combobox"), "Pat"); await userEvent.click(await screen.findByRole("option"));
  expect(screen.getByRole("status")).toHaveTextContent("Adding a teen needs their approval");
  expect(screen.getByRole("button", { name: "Add" })).toBeDisabled();
  expect(Events.addRegistration).not.toHaveBeenCalled();
  actor.user = { id: 10, status: "active", accountType: "teen" };
  view.rerender(<ManageRoster instance={occurrence({})} onChanged={() => {}} onClose={() => {}} />);
  expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
});

it.each(["childcare_full", "people_selection_unavailable", "forbidden", "teen_add_approval_required"])("does not bypass %s through an over-capacity retry", async (code) => {
  vi.mocked(Events.addRegistration).mockRejectedValue(new ApiError(409, "private text", { code }));
  render(<ManageRoster instance={occurrence({})} onChanged={() => {}} onClose={() => {}} />);
  await userEvent.type(screen.getByLabelText("Add someone"), "known@example.com"); await userEvent.click(screen.getByRole("button", { name: "Add" }));
  expect(screen.getByRole("alert")).not.toHaveTextContent("private text");
  expect(Events.addRegistration).toHaveBeenCalledTimes(1); expect(window.confirm).not.toHaveBeenCalled();
});

it("preserves explicit over-capacity confirmation and handles seats filled during add", async () => {
  vi.mocked(Events.addRegistration).mockRejectedValueOnce(new ApiError(409, "full", { code: "event_full" })).mockResolvedValueOnce({});
  render(<ManageRoster instance={occurrence({})} onChanged={() => {}} onClose={() => {}} />);
  await userEvent.type(screen.getByLabelText("Add someone"), "known@example.com"); await userEvent.click(screen.getByRole("button", { name: "Add" }));
  expect(window.confirm).toHaveBeenCalledWith("This date filled up. Add them over capacity?");
  expect(Events.addRegistration).toHaveBeenLastCalledWith(7, "2026-12-01", "known@example.com", true);
});
