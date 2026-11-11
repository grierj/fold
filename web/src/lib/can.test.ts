import { describe, expect, it } from "vitest";
import { canAskForContact, canDemoteLeader, canMakeLeader, canRegisterAsGuest, canRemoveMember, groupCanSchedule, isActiveAdult, isNotTeen, publicName } from "./can";

const adult = { id: 1, status: "active", accountType: "adult" } as const;

describe("who may use adult surfaces", () => {
  it("needs an active adult account", () => {
    expect(isActiveAdult(adult)).toBe(true);
    expect(isActiveAdult({ ...adult, accountType: "teen" })).toBe(false);
    expect(isActiveAdult({ ...adult, status: "pending_consent" })).toBe(false);
    expect(isActiveAdult(null)).toBe(false);
    expect(isNotTeen({ ...adult, status: "invited" })).toBe(true);
    expect(isNotTeen({ ...adult, accountType: "teen" })).toBe(false);
    expect(isNotTeen(undefined)).toBe(false);
  });

  it("asks for contact only from another person whose details are hidden", () => {
    expect(canAskForContact(adult, { id: 2 })).toBe(true);
    expect(canAskForContact(adult, { id: 1 })).toBe(false);
    expect(canAskForContact(adult, { id: 2, email: "x@example.com" })).toBe(false);
    expect(canAskForContact(adult, { id: 2, phone: "" })).toBe(false);
    expect(canAskForContact({ ...adult, accountType: "teen" }, { id: 2 })).toBe(false);
  });
});

describe("member menu", () => {
  it("never makes a teen a leader or demotes the last leader", () => {
    expect(canMakeLeader({ role: "member" })).toBe(true);
    expect(canMakeLeader({ role: "member", accountType: "teen" })).toBe(false);
    expect(canMakeLeader({ role: "leader" })).toBe(false);
    expect(canDemoteLeader({ role: "leader" }, 1, true)).toBe(false);
    expect(canDemoteLeader({ role: "leader" }, 2, false)).toBe(true);
    expect(canDemoteLeader({ role: "leader", admin: true }, 2, false)).toBe(false);
    expect(canDemoteLeader({ role: "member" }, 2, true)).toBe(false);
  });

  it("removes others, not the last leader, and an admin only by an admin", () => {
    expect(canRemoveMember(adult, { id: 2, role: "member" }, 1, false)).toBe(true);
    expect(canRemoveMember(adult, { id: 1, role: "member" }, 1, false)).toBe(false);
    expect(canRemoveMember(adult, { id: 2, role: "leader" }, 1, true)).toBe(false);
    expect(canRemoveMember(adult, { id: 2, role: "leader" }, 2, false)).toBe(true);
    expect(canRemoveMember(adult, { id: 2, role: "member", admin: true }, 1, false)).toBe(false);
    expect(canRemoveMember(adult, { id: 2, role: "member", admin: true }, 1, true)).toBe(true);
  });
});

it("offers the guest form only to a signed-out visitor on a public event", () => {
  expect(canRegisterAsGuest(false, "public")).toBe(true);
  expect(canRegisterAsGuest(true, "public")).toBe(false);
  expect(canRegisterAsGuest(false, "members")).toBe(false);
  expect(canRegisterAsGuest(false, undefined)).toBe(false);
});

it("names a person by display name, then name, then Member, never the email", () => {
  expect(publicName({ displayName: "Sam", name: "Samuel Jones" })).toBe("Sam");
  expect(publicName({ displayName: "", name: "Samuel Jones" })).toBe("Samuel Jones");
  expect(publicName({ displayName: "", name: "" })).toBe("Member");
  expect(publicName({})).toBe("Member");
});

it("offers scheduling only for an active group", () => {
  expect(groupCanSchedule("active")).toBe(true);
  expect(groupCanSchedule("pending")).toBe(false);
  expect(groupCanSchedule("archived")).toBe(false);
  expect(groupCanSchedule("")).toBe(false);
});
