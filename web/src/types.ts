// TypeScript mirrors of the /api/v1 JSON contract (docs/API.md + api.go).

export interface User {
  id: number;
  username: string;
  name: string;
  email: string;
  phone: string;
  role: string;
}

export interface GroupSummary {
  id: number;
  name: string;
  description: string;
  tags: string[];
  imageUrl: string;
  location: string;
  leaderName: string;
  memberCount: number;
  nextMeetingAt: string | null;
  status: string;
}

export interface Member {
  id: number;
  name: string;
  role: "leader" | "member" | string;
  joinedAt: string;
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
