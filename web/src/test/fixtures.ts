import type { ContactRequest, EventSeries, GroupDetailResponse, GroupSummary, Invitation, SiteInfo, User } from "../types";

export function testUser(over: Partial<User> = {}): User {
  return {
    id: 1, name: "Sam", displayName: "", email: "sam@example.com", phone: "", hideContactFromLeaders: false,
    avatarUrl: "", avatarFocalX: 50, avatarFocalY: 50, role: "member", status: "active", hasPassword: true,
    accountType: "adult", timezone: "UTC", ...over,
  };
}

export function testSite(over: Partial<SiteInfo> = {}): SiteInfo {
  return { name: "Grace", timezone: "America/Boise", selfSignup: "off", roomsEnabled: false, pushEnabled: true, ...over };
}

export function testGroup(over: Partial<GroupSummary> = {}): GroupSummary {
  return {
    id: 3, name: "Choir", description: "", tags: [], imageUrl: "", coverFocalX: 50, coverFocalY: 50, location: "",
    leaderName: "", memberCount: 0, nextEventAt: null, status: "active", leaderId: 1, visibility: "public",
    deletionRequested: false, memberLimit: null, ...over,
  };
}

export function testGroupDetail(over: Partial<GroupSummary> = {}): GroupDetailResponse {
  return { group: testGroup(over), members: null, upcomingEvents: [], truncated: false, hasMore: false, nextCursor: "", canManage: true, isMember: true };
}

export function testEvent(id: number): EventSeries {
  return {
    id, title: "Choir", description: "", startsLocal: "2026-12-01T12:00", durationMinutes: 60, tzid: "UTC",
    startsAt: "2026-12-01T12:00:00Z", endsAt: "2026-12-01T13:00:00Z", location: "", recurrenceRule: "",
    visibility: "public", leaderOpened: false, churchWide: false, capacity: null, waitlistEnabled: false,
    guestsAllowed: 0, reminderEmailMinutes: 1440, reminderPush: true, childcareAvailable: false,
    childcareCapacity: null, childcareAges: "", childcareNotes: "", remoteAvailable: false, remoteCapacity: null,
    videoProvider: "none", videoReveal: "registered_only",
  };
}

export function testInvitation(): Invitation {
  return { id: 1, accountType: "adult", conversion: false, email: "sam@example.com", name: "Sam", expiresAt: "2026-12-01T12:00:00Z" };
}

export function testContactRequest(id: number): ContactRequest {
  return {
    id, requesterId: 1, requesterName: "Sam", subjectUserId: 2, subjectName: "Pat", contextType: "group",
    fields: "email", reason: "", needsAdmin: false, subjectDecision: "pending", adminDecision: "not_needed",
    status: "pending", createdAt: "2026-12-01T12:00:00Z", expiresAt: "2026-12-08T12:00:00Z",
    endReason: "", canSubjectDecide: false, canAdminDecide: false, canWithdraw: true,
  };
}
