// TypeScript mirrors of the /api/v1 JSON contract (docs/API.md + api.go).

export interface TeenConsent {
  id: number;
  teenUserId: number;
  scope: "account" | "sms" | "photos";
  status: "granted" | "revoked";
  grantedBy: number;
  granterRole: "guardian" | "admin";
  evidenceText: string;
  at: string;
}

export interface User {
  id: number;
  name: string;
  displayName: string;
  email: string;
  phone: string;
  hideContactFromLeaders: boolean;
  avatarUrl: string;
  avatarFocalX: number;
  avatarFocalY: number;
  role: string;
  status: "active" | "invited" | "guest" | "pending_consent" | "disabled";
  hasPassword: boolean;
  accountType: "adult" | "teen";
  birthDate?: string;
  hasTeenConsentHistory?: boolean;
  // "" until the browser fills it in; then the person's zone for emails (DEC-58).
  timezone: string;
  // Admin list only: Fold stopped emailing this address (M3.2c, DEC-113).
  emailSuppression?: EmailSuppression;
}

// Why Fold stopped emailing an address (DEC-112): hard_bounce and manual stop
// all email; complaint stops optional email, and account email still goes.
export interface EmailSuppression {
  reason: "hard_bounce" | "complaint" | "manual";
  since: string;
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
  status: string;
  leaderId: number;
  visibility: Visibility;
  deletionRequested: boolean;
  // Cap on active members (DEC-39); null means no limit.
  memberLimit: number | null;
  // The viewer's own role, on the directory and My groups lists only (DEC-94).
  myRole?: "leader" | "member";
}

// Who can see a group (C3). invite_only groups are hidden from everyone but
// their members and admins.
export type Visibility = "public" | "private" | "invite_only";

// How a registration attends (M1.8 slice 2): each mode counts against
// its own seat pool.
export type AttendanceMode = "in_person" | "remote";

// Video links (5.8, M1.8): a pasted link or a generated Jitsi room, and
// who may see it. videoUrl arrives only when the viewer may see it.
export type VideoProvider = "none" | "link" | "jitsi";
export type VideoReveal = "always" | "registered_only";

export interface Member {
  email?: string;
  phone?: string;
  accountType?: "teen";
  id: number;
  name: string;
  avatarUrl: string;
  avatarFocalX: number;
  avatarFocalY: number;
  role: "leader" | "member" | string;
  joinedAt: string;
  admin: boolean; // church admin: labeled on rosters, never removable by leaders
}

/** One safe, account-owned date that may be retained during group departure. */
export interface DepartureKeepableDate {
  occurrenceId: number;
  date: string;
  title: string;
  startsAt: string;
  endsAt: string;
  status: "going" | "maybe" | "waitlisted";
  visibility: Visibility;
  defaultKeep: boolean;
  keepReason: "underway" | "cutoff" | "public";
}

/** @public Go JSON contract, checked by TestWebTypesMatchGoJSON. */
export interface DeparturePreview {
  action: "leave" | "remove";
  groupId: number;
  groupName: string;
  subjectId: number;
  subjectName: string;
  affectedCount: number;
  goingCount: number;
  maybeCount: number;
  waitlistedCount: number;
  keepableCount: number;
  defaultKeepCount: number;
  dates: DepartureKeepableDate[];
  snapshot: string;
}

/** @public Go JSON contract, checked by TestWebTypesMatchGoJSON. */
export interface DepartureConfirmation {
  action: "leave" | "remove";
  groupId: number;
  subjectId: number;
  keptCount: number;
  endedCount: number;
}

export interface DepartureConfirmationResponse {
  departure: DepartureConfirmation;
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

/** @public Go JSON contract, checked by TestWebTypesMatchGoJSON. */
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

export type RegistrationStatus = "going" | "maybe" | "declined" | "waitlisted" | "removed" | "cancelled";

// A guest's registration as the confirm and manage pages see it (M1.4):
// the event fitted for a signed-out visitor, with no email and no
// attendee names. status is going, waitlisted (M1.5), or cancelled after
// the manage link cancels it.
export interface GuestRegistration {
  eventId: number;
  occurrenceId: number;
  date: string;
  title: string;
  hostedBy?: string;
  groupName?: string;
  status: string;
  startsAt: string;
  location: string;
  waitlistPosition?: number;
}

// One attendee entry on an occurrence (M1.5 adds guestCount).
/** @public Go JSON contract, checked by TestWebTypesMatchGoJSON. */
export interface Attendee {
  addedByLeader?: boolean;
  email?: string;
  phone?: string;
  accountType?: "adult" | "teen";
  registrationId: number;
  userId?: number;
  householdPerson?: HouseholdAttendee;
  childcareRequested?: boolean;
  name: string;
  status: RegistrationStatus | "not_restored";
  guestCount: number;
  attendanceMode: AttendanceMode;
  childcareCount?: number;
  childcareAgesNote?: string;
}

// One date of an event. date is the series date and stays put when a
// per-date edit moves the clock. startsAt/endsAt are UTC; startsLocal and
// endsLocal are the wall clock in tzid.
export interface EventOccurrence {
 dateUrl?: string;
 groupVisibility?: Visibility;
 visibilityOverride?: Visibility;
 seriesVisibility?: Visibility;
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
  // The date's confirmed room, shown as its place (M2.1b).
  room?: OccurrenceRoom;
  // False means the caller gets a count only. Names are omitted in that case.
  attendeesVisible?: boolean;
  attendees?: Attendee[];
  myHouseholdRegistrations: MyHouseholdRegistration[];
  removedRegistrations?: Attendee[];
  // Capacity, waitlist, and guests (M1.5). capacity is the effective cap
  // for this date; null means unlimited. spotsLeft is null when
  // unlimited and never below 0. seatsUsed counts seats while
  // registrations counts rows.
  capacity: number | null;
  seatsUsed: number;
  spotsLeft: number | null;
  waitlistEnabled: boolean;
  guestsAllowed: number;
  // The series' reminder time (M3.6), for its Edit series form; 0 is off.
  // Optional in fixtures; the API always sends it.
  reminderEmailMinutes?: number;
  // Remote attendance (M1.8 slice 2): the second seat pool, mirroring
  // the in-person fields. remoteCapacity is this date's effective cap.
  childcareAvailable: boolean;
  childcareFull: boolean;
  childcareCapacity: number | null;
  childcareAges: string;
  childcareNotes: string;
  childcareUsed?: number;
  childcareSpotsLeft?: number;
  myChildcareCount?: number;
  myChildcareAgesNote?: string;
  remoteAvailable: boolean;
  remoteCapacity: number | null;
  remoteSeatsUsed: number;
  remoteSpotsLeft: number | null;
  myGuestCount?: number;
  myWaitlistPosition?: number;
  myAttendanceMode?: AttendanceMode;
  // Video link for this date (M1.8). videoUrl is set only when you may
  // see it; videoHidden means a link exists but you can't yet.
  videoProvider: VideoProvider;
  videoUrl?: string;
  videoReveal: VideoReveal;
  videoHidden?: boolean;
}

export interface GroupDetailResponse {
  group: GroupSummary;
  members: Member[] | null;
  upcomingEvents: EventOccurrence[];
  truncated: boolean;
  hasMore: boolean;
  nextCursor: string;
  canManage: boolean;
  isMember: boolean;
}

export interface GroupEventsResponse {
  events: EventOccurrence[];
  truncated: boolean;
  hasMore: boolean;
  nextCursor: string;
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
  // Capacity, waitlist, and guests (M1.5). capacity null means unlimited.
  capacity: number | null;
  waitlistEnabled: boolean;
  guestsAllowed: number;
  // When each date's email reminder goes, in minutes before (M3.6,
  // DEC-133): 0 off, 120, 1440 (the default), 2880 or 10080.
  reminderEmailMinutes: number;
  // Remote attendance (M1.8 slice 2). remoteCapacity null is unlimited.
  childcareAvailable: boolean;
  childcareCapacity: number | null;
  childcareAges: string;
  childcareNotes: string;
  remoteAvailable: boolean;
  remoteCapacity: number | null;
  // Video link (M1.8). At series level it shows under "always" and to
  // the event's leaders or hosts; people going to a date get it there.
  videoProvider: VideoProvider;
  videoUrl?: string;
  videoReveal: VideoReveal;
  videoHidden?: boolean;
}

// The register endpoint's registration object (M1.5): the final status,
// the guest count, and the waitlist position (null unless waitlisted).
export interface RegistrationResponse {
  registrationId: number;
  userId: number;
  eventId: number;
  occurrenceId: number;
  date: string;
  status: RegistrationStatus;
  guestCount: number;
  childcareCount: number;
  childcareAgesNote: string;
  waitlistPosition: number | null;
  attendanceMode: AttendanceMode;
}

export interface EventDetailResponse {
  event: EventSeries;
  occurrences: EventOccurrence[];
  canManage: boolean;
  canSetVisibility: boolean;
}

export interface SeriesRegistrationState {
  registrationId?: number;
  status?: RegistrationStatus;
  guestCount?: number;
  attendanceMode?: AttendanceMode;
  childcareCount?: number;
  childcareAgesNote?: string;
  waitlistPosition?: number;
}

export interface SeriesRegistrationResult {
  occurrenceId: number;
  date: string;
  before: SeriesRegistrationState;
  after: SeriesRegistrationState;
}

export interface SeriesRegistrationRefusal {
  occurrenceId: number;
  date: string;
  code: string;
}

export interface SeriesRegistrationResponse {
  eventId: number;
  scope: "all_future" | "this_and_following" | "";
  date?: string;
  status: RegistrationStatus | "";
  results: SeriesRegistrationResult[];
  refusals: SeriesRegistrationRefusal[];
  undoToken?: string;
  undoExpiresAt?: string;
  capacityLoss?: string[];
}

// A group the person being deleted leads alone, and who may take over (DEC-36).
export interface SoleLedGroup {
  id: number;
  name: string;
  status: string;
  candidates: { id: number; name: string; admin: boolean }[];
}

// What making a group more private does to its events (DEC-48).
/** @public Go JSON contract, checked by TestWebTypesMatchGoJSON. */
export interface VisibilityImpactEvent {
 date?: string;
 occurrenceId?: number;
 decisionKey?: string;
  id: number;
  title: string;
  visibility: Visibility;
  // People registered for upcoming dates who would lose access if tightened.
  registrants: number;
}

export interface VisibilityImpact {
 futureOverrides?: number;
  visibility: Visibility;
  tightened: VisibilityImpactEvent[];
  leaderOpened: VisibilityImpactEvent[];
}

export type OpenedDecision = "keep" | "tighten";

// An invitation to join Fold (M1.2, 5.10). Admins and leaders invite; the
// person sets a password on acceptance and gets an active account.
export interface Invitation {
  conversion: boolean;
  accountType: "adult" | "teen";
  id: number;
  email: string;
  name: string;
  groupId?: number;
  groupName?: string;
  expiresAt: string;
}

export type AgeBand = "adult" | "teen" | "under13";

export interface InvitationAcceptance {
  user?: User;
  pendingConsent: boolean;
  pendingApproval: boolean;
}

export interface InvitationContext {
  invitation: Invitation;
  inviterName: string;
  siteName: string;
  site: string;
}

export interface HouseholdConversion {
  email: string;
  expiresAt: string;
  state: "pending" | "expired" | "awaiting_approval" | "awaiting_consent";
}
/** @public Go JSON contract, checked by TestWebTypesMatchGoJSON. */
export type AccountRequestStatus = "pending" | "approved" | "rejected" | "expired" | "withdrawn";

/** @public Go JSON contract, checked by TestWebTypesMatchGoJSON. */
export interface RequestWarnings {
  duplicate: boolean;
  honeypot: boolean;
  rateLimited: boolean;
}

// One account request in the admin queue (M1.3, DEC-4). Warnings decide
// whether the bell may approve it inline (DEC-44); the queue always can.
export interface AccountRequest {
  guardianEmail?: string;
  id: number;
  name: string;
  email: string;
  phone: string;
  accountType: "adult" | "teen";
  birthDate?: string;
  message: string;
  source: "self_signup" | "member_invite" | "guest_upgrade" | "household_conversion";
  requestedBy?: number;
  requestedByName?: string;
  groupId?: number;
  status: AccountRequestStatus;
  hasWarnings: boolean;
  warnings: RequestWarnings;
  decisionNote: string;
  expiresAt: string;
  createdAt: string;
  decidedAt?: string;
}

// One row of the calendar (GET /calendar, M1.6): no attendee data at all.
// myStatus is the viewer's own answer for this date.
export interface CalendarOccurrence {
  eventId: number;
  date: string;
  // Exact-date link for an invitation that grants no series access.
  dateUrl?: string;
  startsAt: string;
  endsAt: string;
  title: string;
  location: string;
  groupId?: number;
  groupName?: string;
  churchWide: boolean;
  // Offers remote seats on this date; false for a zero per-date limit.
  remoteAvailable: boolean;
  childcareAvailable: boolean;
  hostedBy?: string;
  myStatus: "going" | "maybe" | "waitlisted" | "declined" | null;
  // True only for the viewer's standing/answered/household/exact-date scope.
  // It is presentation data, never authorization.
  ownScope: boolean;
  // Set only for an admin who turned on "Show deleted events" (DEC-68).
  groupDeleted?: boolean;
}

// The profile's calendar feed link (M1.7, 8.2). The server stores only a
// hash, so url is set only in the response that creates or remakes the
// link; everywhere else hasToken and scope are all there is.
export interface CalendarFeed {
  hasToken: boolean;
  scope: "mine" | "all";
  url?: string;
}

// Households (M1.9). Mirrors householdJSON and friends in api_profile.go.
export interface HouseholdPerson {
  conversion?: HouseholdConversion;
  canConvert: boolean;
  age?: number;
  id: number;
  householdId: number;
  firstName: string;
  lastName: string;
  name: string;
  kind: "child" | "teen" | "adult";
  birthDate?: string;
  careNotes?: string;
  registrationEligibility: "eligible" | "link_pending" | "link_unconfirmed" | "birth_date_required" | "age_mismatch" | "converted" | "registration_holder_unavailable";
  photoConsent: boolean;
  linkStatus: "pending" | "confirmed" | "rejected" | "revoked" | "none";
  guardianLinkId?: number;
  convertedUserId?: number;
  createdBy: number;
}

export interface HouseholdAccount {
  status: string;
  userId: number;
  name: string;
  relation: "guardian" | "adult" | "teen";
}

export interface Household {
  id: number;
  name: string;
  address: string;
  createdBy: number;
  people: HouseholdPerson[];
  accounts: HouseholdAccount[];
}

// Mirrors guardianLinkJSON in api_admin.go.
export interface GuardianLink {
  id: number;
  guardianUserId: number;
  guardianName: string;
  teenUserId?: number;
  personId?: number;
  personName: string;
  personKind: "child" | "teen";
  birthDate?: string;
  householdId?: number;
  householdName?: string;
  status: "pending" | "confirmed" | "rejected" | "revoked";
  checkNote: string;
  confirmedBy?: number;
  createdAt: string;
  decidedAt?: string;
}

export interface GuardianNomination {
  requestId: number;
  teenUserId: number;
  teenName: string;
  guardianEmail: string;
}

export interface GuardianTeen {
  linkId: number;
  teenUserId: number;
  teenName: string;
  accountConsent: boolean;
  smsConsent: boolean;
  photosConsent: boolean;
}

export interface GuardianEmailPreview {
  teenName: string;
  scope: "account";
  status: "granted";
  expiresAt: string;
}

export interface HouseholdRegistration {
  registrationId: number;
  personId: number;
  registeredBy: number;
  status: RegistrationStatus;
  attendanceMode: AttendanceMode;
  waitlistPosition: number | null;
  childcareRequested: boolean;
}

export interface RegistrationBatch {
  registration: RegistrationResponse | null;
  people: HouseholdRegistration[];
}

export interface RegisteredBy {
  userId: number;
  name: string;
}

export interface HouseholdAttendee {
  personId: number;
  name: string;
  householdName: string;
  kind: "child" | "teen" | "adult";
  age?: number;
  registeredBy: RegisteredBy;
  careNotes?: string;
  careNotesVisibleUntil?: string;
}

export interface MyHouseholdRegistration {
  registrationId: number;
  personId: number;
  registeredBy: number;
  holderName: string;
  status: RegistrationStatus | "unavailable";
  attendanceMode: AttendanceMode;
  waitlistPosition: number | null;
  childcareRequested: boolean;
  unavailable: boolean;
}

export interface RegistrationCommand {
  date: string;
  status?: RegistrationStatus;
  guestCount?: number;
  attendanceMode?: AttendanceMode;
  childcareCount?: number;
  childcareAgesNote?: string;
  people?: HouseholdAnswer[];
}

export interface HouseholdAnswer {
  personId: number;
  status: "going" | "maybe" | "declined";
  attendanceMode?: AttendanceMode;
  childcareRequested?: boolean;
}

export interface ContactRequest {
  id: number;
  requesterId: number;
  requesterName: string;
  subjectUserId: number;
  subjectName: string;
  contextType: "group" | "event";
  contextId?: number;
  fields: "email" | "phone" | "both";
  reason: string;
  needsAdmin: boolean;
  subjectDecision: "pending" | "approved" | "denied";
  subjectDecidedBy?: number;
  subjectDecidedAt?: string;
  adminDecision: "pending" | "approved" | "denied" | "not_needed";
  adminDecidedBy?: number;
  adminDecidedAt?: string;
  status: "pending" | "granted" | "denied" | "withdrawn" | "expired" | "ended";
  createdAt: string;
  expiresAt: string;
  completedAt?: string;
  endReason: "" | "left_role" | "left_context" | "context_ended" | "turned_18" | "account_disabled" | "event_ended";
  canSubjectDecide: boolean;
  canAdminDecide: boolean;
  canWithdraw: boolean;
}

export interface ContactGrant {
  id: number;
  requestId: number;
  requesterId: number;
  requesterName: string;
  subjectUserId: number;
  subjectName: string;
  contextType: "group" | "event";
  contextId?: number;
  fields: "email" | "phone" | "both";
  needsAdmin: boolean;
  grantedAt: string;
  endedAt?: string;
  endReason: "" | "revoked_by_subject" | "revoked_by_admin" | "revoked_by_guardian" | "left_role" | "left_context" | "context_ended" | "turned_18" | "account_disabled" | "event_ended";
  endedBy?: number;
  canReveal: boolean;
  canRevoke: boolean;
}

export interface ContactReveal {
  email?: string;
  phone?: string;
}

export interface PersonSearchResult {
  selectionId: string;
  displayName: string;
  teen: boolean;
  avatarUrl?: string;
  avatarFocalX?: number;
  avatarFocalY?: number;
  sharedGroups: string[];
  otherGroups: string[];
}

export interface PeopleSearchResponse {
  state: 'short_query' | 'throttled' | 'no_results' | 'results';
  people: PersonSearchResult[];
}

/** @public Safe ask preview, checked by TestWebTypesMatchGoJSON; never group/roster/contact authority. */
export interface AddRequest {
  id: number;
  purpose: "group_add" | "occurrence_add";
  eventTitle?: string;
  date?: string;
  startsAt?: string;
  endsAt?: string;
  tzid?: string;
  completionProblem?: "capacity" | "childcare" | "options" | "throttled";
  requesterName?: string;
  subjectName?: string;
  contextId?: number;
  groupName?: string;
  reason: string;
  overLimit: boolean;
  needsAdmin: boolean;
  subjectDecision: "pending" | "approved" | "denied";
  adminDecision: "pending" | "approved" | "denied" | "not_needed";
  status: "pending" | "granted" | "denied" | "withdrawn" | "expired" | "ended";
  createdAt: string;
  expiresAt: string;
  completedAt?: string;
  endReason: string;
  canSubjectDecide: boolean;
  canAdminDecide: boolean;
  canWithdraw: boolean;
  canComplete: boolean;
}

/** @public Exact-date read contract; does not authorize a mutation. */
export interface EventDate {
 canRegister?: boolean;
 visibility?: Visibility;
 hostedBy?: string;
 groupId?: number;
 groupName?: string;
 room?: OccurrenceRoom;
 // This date's email reminders (M3.6b), only for its event's managers.
 reminders?: DateReminders;
  id: number;
  eventId: number;
  date: string;
  dateUrl: string;
  title: string;
  location: string;
  startsAt: string;
  endsAt: string;
  startsLocal: string;
  endsLocal: string;
  tzid: string;
  durationMinutes: number;
  remoteAvailable: boolean;
  guestsAllowed: number;
  myRegistration: RegistrationStatus | null;
  myAttendanceMode?: AttendanceMode;
  myGuestCount?: number;
  videoProvider: VideoProvider;
  videoUrl?: string;
  videoHidden: boolean;
  canSeeVideo: boolean;
  canSeeAttendees: boolean;
  canDecline: boolean;
  canBlock: boolean;
}

/** @public Narrow administrative person picker, checked by TestWebTypesMatchGoJSON. */
export interface ContactHistoryPerson {
  id: string;
  name: string;
  email: string;
}

/** @public Administrative person picker response, checked by TestWebTypesMatchGoJSON. */
export interface ContactHistoryPeopleResponse {
  people: ContactHistoryPerson[];
}

/** @public Retained safeguarding projection, checked by TestWebTypesMatchGoJSON; no contact values or free text. */
export interface ContactHistoryEntry {
  id: string;
  at: string;
  action: string;
  actorId?: string;
  requesterId?: string;
  subjectUserId?: string;
  purpose?: string;
  requestId?: string;
  accessId?: string;
  contextType?: string;
  contextId?: string;
  occurrenceId?: string;
  fields?: string;
  outcome: string;
  party?: string;
  decision?: string;
}

/** @public Administrative history page, checked by TestWebTypesMatchGoJSON. */
export interface ContactHistoryResponse {
  entries: ContactHistoryEntry[];
  nextBefore?: string;
}

// Me > Notifications (DEC-97): one switch for now. Optional email is every
// notification email except account, safety and requests waiting on you.
// One optional kind of email and whether the person turned it off (DEC-115).
export interface EmailKind {
  category: "admin" | "groups" | "events" | "messages";
  off: boolean;
}

export interface NotificationSettings {
  optionalEmailOff: boolean;
  kinds: EmailKind[];
}

// A bookable room (M2.1a). Admins manage rooms; notes are for admins only.
// Bookings copy the default setup and cleanup minutes (DEC-101).
export interface Room {
  id: number;
  name: string;
  building: string;
  capacity: number | null;
  features: string;
  notes: string;
  active: boolean;
  restricted: boolean;
  defaultSetupMinutes: number;
  defaultTeardownMinutes: number;
}

// A room's calendar-feed link (M2.2b). url is only in the response that
// made the link; afterwards an admin sees only that a link is on.
export interface RoomFeed {
  hasToken: boolean;
  createdAt: string | null;
  url?: string;
}

// The by-room view (M2.2a, 8.1): every active room the viewer may see
// listed (restricted rooms for admins only), with its bookings over one day
// or one week in the church's zone. from and to are the first and last day.
export interface RoomCalendar {
  timezone: string;
  from: string;
  to: string;
  rooms: RoomCalendarRoom[];
  truncated: boolean;
}

export interface RoomCalendarRoom {
  id: number;
  name: string;
  building: string;
  capacity: number | null;
  restricted: boolean;
  bookings: RoomSlot[];
}

// One booking in the by-room view. A date the viewer can't see is only
// its times and reserved: true, never a title, group or link. waiting marks
// a booking that waits for an admin's approval (admins only, DEC-109).
export interface RoomSlot {
  startsAt: string;
  endsAt: string;
  reserved?: boolean;
  eventId?: number;
  date?: string;
  dateUrl?: string;
  title?: string;
  groupId?: number;
  groupName?: string;
  hostedBy?: string;
  churchWide?: boolean;
  waiting?: boolean;
}

// The room a date is booked in, shown as its place (M2.1b, 9.2).
export interface OccurrenceRoom {
  id: number;
  name: string;
  building: string;
}

// A room in a date's room picker (M2.1b). Busy and too-small never say who
// holds the room; too-small is a warning, not a block.
export interface RoomPickerItem {
  id: number;
  name: string;
  building: string;
  capacity: number | null;
  features: string;
  restricted: boolean;
  busy: boolean;
  tooSmall: boolean;
}

// A date's booking as the people running the event see it (M2.1b).
export interface RoomBooking {
  roomId: number;
  roomName: string;
  building: string;
  status: "confirmed" | "requested" | "rejected";
  setupMinutes: number;
  teardownMinutes: number;
  doubleBooked: boolean;
  tooSmall: boolean;
  // Requested and waiting on an admin's approval (M2.1d). A requested
  // booking that isn't pending lost its room.
  pending: boolean;
}

// A room as the whole-series picker sees it (M2.1c): busyDates counts the
// dates another booking holds it on, never saying whose.
export interface SeriesRoom {
  id: number;
  name: string;
  building: string;
  capacity: number | null;
  features: string;
  restricted: boolean;
  busyDates: number;
  tooSmall: boolean;
}

// Room suggestions (M2.3, PLAN 9.3, DEC-110): up to 5 rooms best first, each
// with ready-made copy for why ("Used by this group 6 times"). Only room ids
// and counts, never another event's title, group or date. otherKinds: none
// matched the group's kind, so these are "Used by other kinds of events".
// busyDates counts how many of `dates` checked dates the room is taken on.
export interface RoomSuggestion {
  roomId: number;
  uses: number;
  reason: string;
  busyDates: number;
}

export interface RoomSuggestions {
  rooms: RoomSuggestion[];
  otherKinds: boolean;
  dates: number;
}

// A date whose booking holds nothing: it lost its room when it moved, or
// the room was busy when the series was booked (DEC-103).
export interface NeedsRoom {
  date: string;
  roomName: string;
  // requested: it lost its room; rejected: an admin didn't approve it (M2.1d).
  status: "requested" | "rejected";
}

// What booking a whole series did (M2.1c).
export interface SeriesBooking {
  defaultRoom: OccurrenceRoom | null;
  booked: number;
  lost: string[];
  kept: number;
  // Dates waiting on an admin's approval (M2.1d).
  pending: number;
}

// A typed place on upcoming dates without a room, for the admin location
// matcher (M2.1e). Spellings that differ only in case or spaces are one.
export interface TypedLocation {
  location: string;
  dates: number;
  events: number;
}

// A matching date the room was taken on when matching a location (M2.1e).
export interface MatchBusy {
  eventId: number;
  eventTitle: string;
  date: string;
}

// An open room request in the admin list (M2.1d, DEC-106): one booking
// action, a date or a whole series. busyDates counts its dates another
// booking now holds the room on; confirming those needs a reason.
export interface RoomRequest {
  id: number;
  eventId: number;
  eventTitle: string;
  groupName: string;
  roomId: number;
  roomName: string;
  building: string;
  series: boolean;
  requestedBy: string;
  dates: number;
  busyDates: number;
  firstDate: string;
  // When the first waiting date starts (UTC).
  firstStartsAt: string;
  createdAt: string;
}

// A background job that ran out of attempts (M3.1b, DEC-111), as the admin
// System page shows it. label says what it does; runsDaily means tomorrow's
// run tries again by itself. Never its payload.
export interface AdminJob {
  id: number;
  kind: string;
  label: string;
  runsDaily: boolean;
  attempts: number;
  lastError: string;
  failedAt: string;
}

// --- Messages: the composer (M3.4) ---

type MessageStatus = "draft" | "scheduled" | "sending" | "sent" | "failed" | "cancelled";
type AudienceType = "occurrence" | "event_series" | "events" | "group" | "all_members" | "users";
export type AudienceStatus = "going" | "maybe" | "waitlisted";
export type AudienceRole = "member" | "leader";

// Who a message goes to (PLAN 7.2). Only the fields its type uses are set.
export interface MessageAudience {
  type: AudienceType;
  occurrenceId?: number;
  eventId?: number;
  eventIds?: number[];
  groupId?: number;
  userIds?: number[];
  statuses?: AudienceStatus[];
  roles?: AudienceRole[];
}

export interface Message {
  id: number;
  channel: "email" | "sms";
  status: MessageStatus;
  subject: string;
  bodyHtml: string;
  audience: MessageAudience;
  scheduledAt: string | null;
  sentAt: string | null;
  authorName: string;
  // Still a draft or scheduled, and the viewer may change it.
  editable: boolean;
  updatedAt: string;
  // Why it failed, or why some people didn't get it (M3.5); "" otherwise.
  error: string;
  // How many people the send failed for: "Partly sent" when it was sent.
  failedCount: number;
  // An automatic event reminder (M3.6) has no author and is never
  // editable; reminder names its date (absent once the date is gone).
  category: "announcement" | "reminder";
  reminder?: MessageReminder;
  // A reminder's subject with its event fields filled as its emails said
  // them, for its page's heading; "" for an announcement.
  displaySubject: string;
}

// The date an automatic reminder is for. kind is the going reminder or
// the waitlist note (DEC-133).
export interface MessageReminder {
  kind: ReminderKind;
  occurrenceId: number;
  eventId: number;
  eventTitle: string;
  date: string;
  startsAt: string;
  // The event's zone: its emails' times and the date shown read in it.
  tzid: string;
}

export type ReminderKind = "going" | "waitlist";

// A date's email reminders as its event's managers see them on the date
// page (M3.6b): the event's reminder time (0 off), when this date's
// reminder goes (null when off), and each reminder written for it.
export interface DateReminders {
  minutes: number;
  dueAt: string | null;
  // A reminder already sent covers the one due, so it won't go.
  covered: boolean;
  messages: DateReminder[];
}

// One reminder message: how many people it reached (guardian copies
// apart, DEC-132) and failed for.
export interface DateReminder {
  messageId: number;
  kind: ReminderKind;
  status: MessageStatus;
  sentAt: string | null;
  reached: number;
  // failed: bounced, marked as spam or failed; skipped: never sent (M3.5b's counts).
  failed: number;
  skipped: number;
}

// The people a sent message was for, by status (M3.5b). skipped never went
// out (counted by reason in skippedBy); teens are the teens it was for.
// Guardian copies are counted apart (DEC-132): guardianCopiesMissed are the
// copies that didn't go out, guardianCopiesFailed the failed ones among them.
export interface MessageRecipientCounts {
  sent: number;
  delivered: number;
  bounced: number;
  complained: number;
  failed: number;
  skipped: number;
  queued: number;
  teens: number;
  guardianCopies: number;
  guardianCopiesMissed: number;
  guardianCopiesFailed: number;
}

// "missed" is a skip, bounce, complaint or failure: a leader or host sees
// only that; an admin also gets each person's reason.
export type HistoryStatus = "sent" | "delivered" | "missed" | "waiting";

// One person in a message's history: a name, a Teen badge, a guardian
// copy's teen (copyOf, admins only: DEC-132), never an address (DEC-31).
export interface MessageHistoryRecipient {
  name: string;
  teen: boolean;
  status: HistoryStatus;
  reason?: string;
  copyOf?: string;
}

// GET /messages/:id/recipients.
export interface MessageRecipients {
  counts: MessageRecipientCounts;
  skippedBy: Record<string, number>;
  // The first 200 by name; more says there are others.
  recipients: MessageHistoryRecipient[];
  more: boolean;
  // Show "Try again" (DEC-131).
  canRetry: boolean;
}

// One person an audience names: a name and a Teen badge, never an address
// (DEC-20, DEC-31). skipped is why they won't get it ("won't get this" for
// leaders and hosts; the reason for admins).
export interface AudienceRecipient {
  name: string;
  teen: boolean;
  skipped?: string;
}

// The live count under the audience picker: everyone named, how many will
// get it, the teens among them, and the rest counted by reason.
export interface AudienceSummary {
  people: number;
  eligible: number;
  teens: number;
  skipped: Record<string, number>;
  // The first 100 by name; more says there are others.
  recipients: AudienceRecipient[];
  more: boolean;
}

export interface ComposeGroup {
  id: number;
  name: string;
}

export interface ComposeDate {
  occurrenceId: number;
  date: string;
  startsAt: string;
}

export interface ComposeEvent {
  id: number;
  title: string;
  groupName?: string;
  churchWide: boolean;
  // The next few upcoming dates.
  dates: ComposeDate[];
}

// What the author may write to (GET /messages/compose). smsEnabled shows
// the SMS tab, off until texting can be set up (DEC-117).
export interface ComposeOptions {
  canCompose: boolean;
  canMessageEveryone: boolean;
  smsEnabled: boolean;
  groups: ComposeGroup[];
  events: ComposeEvent[];
}

// A saved message as the viewer would get it: the subject and the
// plain-text part, merge fields filled with the viewer's own values.
export interface MessagePreview {
  subject: string;
  text: string;
}
