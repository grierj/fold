import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { Events, Groups, Households } from "../api";
import type { GroupDetailResponse, Household, User } from "../types";
import { occurrence } from "../test/occurrence";
import { GroupDetail } from "./GroupDetail";

const actor = vi.hoisted(() => ({ user: { id: 2, accountType: "adult", hideContactFromLeaders: false } as User | null }));
vi.mock("../auth", () => ({ useAuth: () => ({ user: actor.user, loading: false, isAdmin: false }) }));
vi.mock("../api", async (actual) => ({ ...(await actual<typeof import("../api")>()), Groups: { get: vi.fn() }, Events: { register: vi.fn() }, Households: { mine: vi.fn() } }));
const family: Household = { id: 1, name: "Family", address: "", createdBy: 2, accounts: [], people: [{ id: 3, householdId: 1, canConvert: false, firstName: "Mary", lastName: "", name: "Mary", kind: "adult", photoConsent: false, linkStatus: "none", registrationEligibility: "eligible", createdBy: 2 }] };
const page: GroupDetailResponse = { group: { id: 1, name: "Family group", description: "", tags: [], imageUrl: "", coverFocalX: 50, coverFocalY: 50, location: "Hall", leaderName: "Leader", memberCount: 1, nextEventAt: null, status: "active", leaderId: 1, visibility: "public", deletionRequested: false, memberLimit: null }, members: [], upcomingEvents: [occurrence({})], canManage: false, isMember: true };
function open() { render(<MemoryRouter initialEntries={["/groups/1"]}><Routes><Route path="/groups/:id" element={<GroupDetail />} /></Routes></MemoryRouter>); }
beforeEach(() => { vi.resetAllMocks(); actor.user = { id: 2, accountType: "adult", hideContactFromLeaders: false } as User; vi.mocked(Groups.get).mockResolvedValue(page); vi.mocked(Households.mine).mockResolvedValue({ household: family }); });

it("fetches the household once and refetches all date state after a batch", async () => {
  vi.mocked(Events.register).mockResolvedValue({ registration: null, people: [{ registrationId: 9, personId: 3, registeredBy: 2, status: "going", attendanceMode: "in_person", waitlistPosition: null, childcareRequested: false }] });
  open(); await userEvent.click(await screen.findByRole("checkbox", { name: "Mary" }));
  await userEvent.click(screen.getByRole("button", { name: "Save answers" }));
  await waitFor(() => expect(Groups.get).toHaveBeenCalledTimes(2));
  expect(Households.mine).toHaveBeenCalledTimes(1);
  expect(Events.register).toHaveBeenCalledWith(7, { date: "2026-12-01", people: [{ personId: 3, status: "going", attendanceMode: "in_person", childcareRequested: false }] });
});

it.each([null, { id: 2, accountType: "teen", hideContactFromLeaders: true } as User])("does not fetch household data for an unsigned or teen viewer", async (user) => {
  actor.user = user; open(); await screen.findByRole("heading", { name: "Family group" });
  expect(Households.mine).not.toHaveBeenCalled();
});

it("shows supplied roster contact without reconstructing hidden members' details", async () => {
  const member = { avatarUrl: "", avatarFocalX: 50, avatarFocalY: 50, role: "member", joinedAt: "2026-10-01T00:00:00Z", admin: false };
  vi.mocked(Groups.get).mockResolvedValue({ ...page, members: [
    { ...member, id: 3, name: "Visible adult", email: "visible@example.com", phone: "208-555-0100" },
    { ...member, id: 4, name: "Hidden adult" },
    { ...member, id: 5, name: "Teen member", accountType: "teen" },
  ] });
  open();
  await userEvent.click(await screen.findByRole("button", { name: "members" }));
  expect(screen.getByText("Email: visible@example.com")).toBeInTheDocument();
  expect(screen.getByText("Phone: 208-555-0100")).toBeInTheDocument();
  expect(screen.getAllByText(/Email:/)).toHaveLength(1);
  expect(screen.getAllByText(/Phone:/)).toHaveLength(1);
  expect(screen.getByText("Hidden adult")).toBeInTheDocument();
  expect(screen.getByText("Teen member")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /contact/i })).not.toBeInTheDocument();
});
