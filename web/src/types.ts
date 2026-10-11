// TypeScript mirrors of the /api/v1 JSON contract (docs/API.md + api.go).

export interface User {
  id: number;
  name: string;
  displayName: string;
  email: string;
  phone: string;
  avatarUrl: string;
  avatarFocalX: number;
  avatarFocalY: number;
  role: string;
  status: "active" | "invited" | "guest" | "pending_consent" | "disabled";
  hasPassword: boolean;
}

export interface GroupSummary {
  id: number;
  name: string;
  description: string;
  tags: string[];
  imageUrl: string;
  coverFocalX: number;
  coverFocalY: number;
  location: string;
  leaderName: string;
  memberCount: number;
  nextEventAt: string | null;
  nextEventLocal: string;
  nextEventTzid: string;
  status: string;
  leaderId: number;
  visibility: Visibility;
  deletionRequested: boolean;
  // Cap on active members (DEC-39); null means no limit.
  memberLimit: number | null;
}

// Who can see a group (C3). invite_only groups are hidden from everyone but
// their members and admins.
export type Visibility = "public" | "private" | "invite_only";

export interface Member {
  id: number;
  name: string;
  avatarUrl: string;
  avatarFocalX: number;
  avatarFocalY: number;
  role: "leader" | "member" | string;
  joinedAt: string;
  admin: boolean; // church admin: labeled on rosters, never removable by leaders
}

export interface Notification {
  id: string;
  type: "approval" | "event" | "leadership";
  title: string;
  body: string;
  link: string;
}

export type RegistrationStatus = "going" | "maybe" | "declined";

// One date of an event. date is the series date and stays put when a
// per-date edit moves the clock. startsAt/endsAt are UTC; startsLocal and
// endsLocal are the wall clock in tzid.
export interface EventOccurrence {
  id: number;
  eventId: number;
  date: string;
  startsAt: string;
  endsAt: string;
  startsLocal: string;
  endsLocal: string;
  tzid: string;
  durationMinutes: number;
  location: string;
  title: string;
  recurrenceRule: string;
  rruleUntil?: string;
  seriesStartsLocal: string;
  seriesTitle: string;
  seriesLocation: string;
  seriesDurationMinutes: number;
  overridden: boolean;
  registrations: Record<string, number>;
  myRegistration: RegistrationStatus | null;
  visibility?: Visibility;
  visibilitySource?: "group" | "custom";
  churchWide?: boolean;
  groupId?: number;
  groupName?: string;
  hostedBy?: string;
  // False means the caller gets a count only. Names are omitted in that case.
  attendeesVisible?: boolean;
  attendees?: { userId: number; name: string; status: RegistrationStatus }[];
}

// NOTE: the implementation returns {group, members, upcomingEvents,
// canManage, isMember}.
export interface GroupDetailResponse {
  group: GroupSummary;
  members: Member[] | null;
  upcomingEvents: EventOccurrence[];
  canManage: boolean;
  isMember: boolean;
  // Re-enabled accounts waiting because the group was full (leaders and admins only).
  pendingReturns: Member[];
}

export interface PendingReturn {
  group: GroupSummary;
  member: Member;
}

export interface EventHostRow {
  userId: number;
  name: string;
  email: string;
}

export interface EventSeries {
  id: number;
  groupId?: number;
  groupName?: string;
  groupVisibility?: Visibility;
  hostedBy?: string;
  title: string;
  startsLocal: string;
  durationMinutes: number;
  tzid: string;
  startsAt: string;
  endsAt: string;
  location: string;
  recurrenceRule: string;
  rruleUntil?: string;
  visibility: Visibility;
  visibilitySource: "group" | "custom";
  churchWide: boolean;
  hosts?: EventHostRow[];
}

export interface EventDetailResponse {
  event: EventSeries;
  occurrences: EventOccurrence[];
  canManage: boolean;
  canSetVisibility: boolean;
}

// A group the person being deleted leads alone, and who may take over (DEC-36).
export interface SoleLedGroup {
  id: number;
  name: string;
  status: string;
  candidates: { id: number; name: string; admin: boolean }[];
}
