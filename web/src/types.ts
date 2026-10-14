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

// A stored notification (5.12). Text is plain and rendered by the server;
// link is always an internal path. waiting: an open task, counted in the
// badge until someone resolves it.
export interface Notification {
  id: number;
  kind: string;
  subjectId?: number;
  category: "admin" | "groups" | "events" | "messages" | "safety";
  title: string;
  body: string;
  link: string;
  actions: { key: string; label: string }[];
  batchCount: number;
  createdAt: string;
  updatedAt: string;
  read: boolean;
  archived: boolean;
  waiting: boolean;
  resolution?: NotificationResolution;
}

export interface NotificationResolution {
  outcome: "approved" | "rejected" | "deleted" | "kept" | "withdrawn" | string;
  by: string;
  byMe: boolean;
  at: string;
}

export type NotificationFilter = "all" | "unread" | "waiting" | "archived";

export interface NotificationFeed {
  waiting: Notification[];
  waitingTotal: number;
  notifications: Notification[];
  next: string;
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
  description: string;
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
  // A leader set this event wider than its group (DEC-48).
  leaderOpened?: boolean;
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
  description: string;
  startsLocal: string;
  durationMinutes: number;
  tzid: string;
  startsAt: string;
  endsAt: string;
  location: string;
  recurrenceRule: string;
  rruleUntil?: string;
  visibility: Visibility;
  leaderOpened: boolean;
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

// What making a group more private does to its events (DEC-48).
export interface VisibilityImpactEvent {
  id: number;
  title: string;
  visibility: Visibility;
  // People registered for upcoming dates who would lose access if tightened.
  registrants: number;
}

export interface VisibilityImpact {
  visibility: Visibility;
  tightened: VisibilityImpactEvent[];
  leaderOpened: VisibilityImpactEvent[];
}

export type OpenedDecision = "keep" | "tighten";

// An invitation to join Fold (M1.2, 5.10). Admins and leaders invite; the
// person sets a password on acceptance and gets an active account.
export interface Invitation {
  id: number;
  email: string;
  name: string;
  groupId?: number;
  groupName?: string;
  expiresAt: string;
}
