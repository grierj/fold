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
  nextMeetingAt: string | null;
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

export interface MeetingInstance {
  meetingId: number;
  date: string; // YYYY-MM-DD — the per-instance address
  startsAt: string;
  endsAt: string;
  location: string;
  rsvps: Record<string, number>;
  myRsvp: "going" | "maybe" | "declined" | null;
}

// NOTE: the implementation returns {group, members, upcomingMeetings,
// canManage, isMember} — docs/API.md lists a `leader` field here that the
// handler does not actually send.
export interface GroupDetailResponse {
  group: GroupSummary;
  members: Member[] | null;
  upcomingMeetings: MeetingInstance[];
  canManage: boolean;
  isMember: boolean;
  // Re-enabled accounts waiting because the group was full (leaders and admins only).
  pendingReturns: Member[];
}

export interface PendingReturn {
  group: GroupSummary;
  member: Member;
}

export interface Meeting {
  id: number;
  groupId: number;
  startsAt: string;
  endsAt: string;
  location: string;
  recurrenceRule: string;
  cancelledDates: string[];
}

// A group the person being deleted leads alone, and who may take over (DEC-36).
export interface SoleLedGroup {
  id: number;
  name: string;
  status: string;
  candidates: { id: number; name: string; admin: boolean }[];
}
