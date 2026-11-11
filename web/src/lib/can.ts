import type { User } from "../types";

// The client's copies of server permission rules, in one place. The server
// decides every one of these again (internal/policy); a copy here only decides
// what to offer, so a stale copy hides or shows a button, never grants a
// power. Each function names the policy function it mirrors.

type Viewer = Pick<User, "id" | "status" | "accountType"> | null | undefined;

// policy.ContactAccount: Active and adult. CanInitiatePeopleAdd is exactly
// this, and CanSearchPeople adds the manager fact the page checks itself.
export function isActiveAdult(user: Viewer): user is NonNullable<Viewer> {
  return !!user && user.status === "active" && user.accountType === "adult";
}

// policy.CanCreateHousehold, CanUploadAvatar and the other "!p.Teen" rules:
// teens have no household, room calendar or similar adult-only surfaces.
export function isNotTeen(user: Viewer): user is NonNullable<Viewer> {
  return !!user && user.accountType !== "teen";
}

// The member row a roster or group page shows. email and phone are present
// only when the server let this viewer see them.
type Listed = { id: number; email?: string; phone?: string };

// policy contact requests (contact.go, CanViewContactRequest and friends): an
// active adult may ask another person for contact details they cannot see.
export function canAskForContact(user: Viewer, subject: Listed): boolean {
  return isActiveAdult(user) && subject.id !== user.id && subject.email === undefined && subject.phone === undefined;
}

type RoleTarget = { role: string; admin?: boolean; accountType?: string };

// policy.CanChangeRole with makeLeader: teens never lead (DEC-18).
export function canMakeLeader(target: RoleTarget): boolean {
  return target.role !== "leader" && target.accountType !== "teen";
}

// policy.CanChangeRole demoting a leader: never the last leader, and only an
// admin may demote an admin (DEC-35).
export function canDemoteLeader(target: RoleTarget, leaders: number, viewerIsAdmin: boolean): boolean {
  return target.role === "leader" && leaders > 1 && (!target.admin || viewerIsAdmin);
}

// policy.CanRemoveMember: not yourself, not the last leader, and only an admin
// removes an admin.
export function canRemoveMember(viewer: Viewer, target: RoleTarget & { id: number }, leaders: number, viewerIsAdmin: boolean): boolean {
  return target.id !== viewer?.id && (target.role !== "leader" || leaders > 1) && (!target.admin || viewerIsAdmin);
}

// policy.CanRegisterGuest: a signed-out visitor may register for a public event.
export function canRegisterAsGuest(signedIn: boolean, visibility: string | undefined): boolean {
  return !signedIn && visibility === "public";
}

// models.User.PublicName: the display name, else the full name, else "Member".
// Never the email.
export function publicName(person: { displayName?: string | null; name?: string | null }): string {
  return person.displayName || person.name || "Member";
}

// policy.GroupCanSchedule (DEC-147): new events and room bookings need an
// approved, active group, for leaders and admins alike. Display only; the
// server refuses the write.
export function groupCanSchedule(status: string): boolean {
  return status === "active";
}
