package policy

import (
	"strings"
	"testing"
	"time"
)

func addFixture() (AddScope, AddFacts, AddRequest) {
	_, c, _, _ := contactFixture()
	s := AddScope{Purpose: RequestOccurrenceAdd, RequesterID: 1, SubjectUserID: 2, ContextType: ContactEvent, ContextID: 3,
		OccurrenceID: 4, Options: AddOptions{AttendanceMode: "in_person"}}
	f := AddFacts{Requester: c.Requester, Subject: c.Subject, ContextType: ContactEvent, ContextID: 3, ContextActive: true,
		Manager: true, OrdinaryEligible: true, Now: c.Now,
		Occurrence: OccurrenceFacts{ID: 4, EventID: 3, Organizer: OrganizerScope{ContactGroup, 5}, ContextActive: true, Scheduled: true}}
	r := AddRequest{AddScope: s, NeedsAdmin: true, SubjectDecision: ContactPending, AdminDecision: ContactPending,
		Status: ContactPending, CreatedAt: f.Now.Add(-time.Hour), ExpiresAt: f.Now.Add(ContactRequestLifetime)}
	return s, f, r
}

func adultAdd(f *AddFacts, r *AddRequest) {
	f.Subject.Person.Teen = false
	f.Subject.AgeYears = nil
	r.NeedsAdmin, r.AdminDecision = false, ContactNotNeeded
}

func TestCanCreateAddRequest(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*AddScope, *AddFacts, *int)
		want   bool
	}{
		{"teen even ordinary eligible", func(_ *AddScope, _ *AddFacts, _ *int) {}, true},
		{"teen hidden contact irrelevant", func(_ *AddScope, f *AddFacts, _ *int) { f.Subject.HideFromLeaders = false }, true},
		{"adult needs own approval outside ordinary policy", func(_ *AddScope, f *AddFacts, _ *int) {
			f.Subject.Person.Teen = false
			f.Subject.AgeYears = nil
			f.OrdinaryEligible = false
		}, true},
		{"adult ordinary add stays direct", func(_ *AddScope, f *AddFacts, _ *int) { f.Subject.Person.Teen = false; f.Subject.AgeYears = nil }, false},
		{"admin manager still teen ask", func(_ *AddScope, f *AddFacts, _ *int) { f.Requester.Person.Admin = true }, true},
		{"admin not current manager", func(_ *AddScope, f *AddFacts, _ *int) { f.Requester.Person.Admin = true; f.Manager = false }, false},
		{"group nonmember teen", func(s *AddScope, f *AddFacts, _ *int) {
			s.Purpose = RequestGroupAdd
			s.ContextType = ContactGroup
			s.OccurrenceID = 0
			s.Options = AddOptions{}
			f.ContextType = ContactGroup
		}, true},
		{"group cannot name date", func(s *AddScope, _ *AddFacts, _ *int) {
			s.Purpose = RequestGroupAdd
			s.ContextType = ContactGroup
			s.Options = AddOptions{}
		}, false},
		{"contact purpose rejected", func(s *AddScope, _ *AddFacts, _ *int) { s.Purpose = RequestContact }, false},
		{"empty purpose rejected", func(s *AddScope, _ *AddFacts, _ *int) { s.Purpose = "" }, false},
		{"other event", func(s *AddScope, _ *AddFacts, _ *int) { s.ContextID++ }, false},
		{"other date", func(s *AddScope, _ *AddFacts, _ *int) { s.OccurrenceID++ }, false},
		{"no date", func(s *AddScope, _ *AddFacts, _ *int) { s.OccurrenceID = 0 }, false},
		{"wrong subject", func(s *AddScope, _ *AddFacts, _ *int) { s.SubjectUserID++ }, false},
		{"self add not manager flow", func(s *AddScope, f *AddFacts, _ *int) { s.SubjectUserID = 1; f.Subject.Person.ID = 1 }, false},
		{"lost issuer role", func(_ *AddScope, f *AddFacts, _ *int) { f.Manager = false }, false},
		{"disabled issuer", func(_ *AddScope, f *AddFacts, _ *int) { f.Requester.Active = false }, false},
		{"disabled or consent lost subject", func(_ *AddScope, f *AddFacts, _ *int) { f.Subject.Active = false }, false},
		{"stale teen eighteen", func(_ *AddScope, f *AddFacts, _ *int) { age := 18; f.Subject.AgeYears = &age }, false},
		{"unknown teen age", func(_ *AddScope, f *AddFacts, _ *int) { f.Subject.AgeYears = nil }, false},
		{"under thirteen", func(_ *AddScope, f *AddFacts, _ *int) { age := 12; f.Subject.AgeYears = &age }, false},
		{"inactive context", func(_ *AddScope, f *AddFacts, _ *int) { f.ContextActive = false }, false},
		{"deleted occurrence context", func(_ *AddScope, f *AddFacts, _ *int) { f.Occurrence.ContextActive = false }, false},
		{"cancelled date", func(_ *AddScope, f *AddFacts, _ *int) { f.Occurrence.Scheduled = false }, false},
		{"ended date permits new ask", func(_ *AddScope, f *AddFacts, _ *int) { end := f.Now.Add(-2 * time.Hour); f.LatestAccessEndedAt = &end }, true},
		{"live date prevents duplicate ask", func(_ *AddScope, f *AddFacts, _ *int) { f.ExistingAccessActive = true }, false},
		{"recipient block", func(_ *AddScope, f *AddFacts, _ *int) { f.Block = &ManagerAddBlock{2, f.Occurrence.Organizer} }, false},
		{"other recipient block", func(_ *AddScope, f *AddFacts, _ *int) { f.Block = &ManagerAddBlock{9, f.Occurrence.Organizer} }, true},
		{"other organizer block", func(_ *AddScope, f *AddFacts, _ *int) { f.Block = &ManagerAddBlock{2, OrganizerScope{ContactGroup, 9}} }, true},
		{"same id different organizer kind", func(_ *AddScope, f *AddFacts, _ *int) { f.Block = &ManagerAddBlock{2, OrganizerScope{ContactEvent, 5}} }, true},
		{"churchwide event block", func(_ *AddScope, f *AddFacts, _ *int) {
			f.Occurrence.Organizer = OrganizerScope{ContactEvent, 3}
			f.Block = &ManagerAddBlock{2, f.Occurrence.Organizer}
		}, false},
		{"open cap", func(_ *AddScope, _ *AddFacts, n *int) { *n = 10 }, false},
		{"negative open count", func(_ *AddScope, _ *AddFacts, n *int) { *n = -1 }, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, f, _ := addFixture()
			n := 0
			tt.change(&s, &f, &n)
			if got := CanCreateAddRequest(s, f, n); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestAddRequestDualDecisions(t *testing.T) {
	for _, purpose := range []string{RequestGroupAdd, RequestOccurrenceAdd} {
		for _, first := range []string{ContactAdmin, ContactSubject} {
			for _, firstDecision := range []string{ContactApproved, ContactDenied} {
				for _, secondDecision := range []string{ContactApproved, ContactDenied} {
					t.Run(purpose+"/"+first+"/"+firstDecision+"/"+secondDecision, func(t *testing.T) {
						_, f, r := addFixture()
						if purpose == RequestGroupAdd {
							r.Purpose = purpose
							r.ContextType = ContactGroup
							r.OccurrenceID = 0
							r.Options = AddOptions{}
							f.ContextType = ContactGroup
						}
						admin := f.Requester
						admin.Person.Admin = true // requester admin still cannot skip teen
						actor, secondActor, secondParty := f.Subject, admin, ContactAdmin
						if first == ContactAdmin {
							actor, secondActor, secondParty = admin, f.Subject, ContactSubject
						}
						want := ContactPending
						if firstDecision == ContactDenied {
							want = ContactDenied
						}
						if got := AddRequestStatusAfterDecision(actor, first, firstDecision, r, f); got != want {
							t.Fatalf("first = %q want %q", got, want)
						}
						r.Status = want
						if first == ContactAdmin {
							r.AdminDecision = firstDecision
						} else {
							r.SubjectDecision = firstDecision
						}
						if CanFinalizeAddRequest(r, f) {
							t.Fatal("one-party approval completed teen add")
						}
						want = ContactPending
						if firstDecision == ContactDenied {
							want = ""
						} else if secondDecision == ContactDenied {
							want = ContactDenied
						}
						if got := AddRequestStatusAfterDecision(secondActor, secondParty, secondDecision, r, f); got != want {
							t.Fatalf("second = %q want %q", got, want)
						}
						if want != "" {
							r.Status = want
							if secondParty == ContactAdmin {
								r.AdminDecision = secondDecision
							} else {
								r.SubjectDecision = secondDecision
							}
						}
						if CanFinalizeAddRequest(r, f) != (firstDecision == ContactApproved && secondDecision == ContactApproved) {
							t.Fatal("finalize does not match both approvals")
						}
					})
				}
			}
		}
	}
}

func TestCanFinalizeAddRequest(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*AddRequest, *AddFacts)
		want   bool
	}{
		{"dual approvals ready not success", func(_ *AddRequest, _ *AddFacts) {}, true},
		{"adult alone", func(r *AddRequest, f *AddFacts) { adultAdd(f, r) }, true},
		{"teen forged adult approval", func(r *AddRequest, _ *AddFacts) { r.NeedsAdmin = false; r.AdminDecision = ContactNotNeeded }, false},
		{"actual eighteen stale teen", func(_ *AddRequest, f *AddFacts) { age := 18; f.Subject.AgeYears = &age }, false},
		{"actual eighteen adult rollover", func(_ *AddRequest, f *AddFacts) { age := 18; f.Subject.AgeYears = &age; f.Subject.Person.Teen = false }, false},
		{"expiry exact", func(r *AddRequest, f *AddFacts) { f.Now = r.ExpiresAt }, false},
		{"expiry just before", func(r *AddRequest, f *AddFacts) { f.Now = r.ExpiresAt.Add(-time.Nanosecond) }, true},
		{"denied", func(r *AddRequest, _ *AddFacts) { r.Status = ContactDenied; r.SubjectDecision = ContactDenied }, false},
		{"withdrawn", func(r *AddRequest, _ *AddFacts) { r.Status = ContactWithdrawn }, false},
		{"completed cannot replay", func(r *AddRequest, _ *AddFacts) { r.Status = ContactGranted }, false},
		{"ended no resurrection", func(r *AddRequest, _ *AddFacts) { r.Status = ContactEnded }, false},
		{"expired", func(r *AddRequest, _ *AddFacts) { r.Status = ContactExpired }, false},
		{"blocked after approvals", func(_ *AddRequest, f *AddFacts) { f.Block = &ManagerAddBlock{2, f.Occurrence.Organizer} }, false},
		{"issuer lost role after approvals", func(_ *AddRequest, f *AddFacts) { f.Manager = false }, false},
		{"subject disabled or consent revoked", func(_ *AddRequest, f *AddFacts) { f.Subject.Active = false }, false},
		{"cancelled after approvals", func(_ *AddRequest, f *AddFacts) { f.Occurrence.Scheduled = false }, false},
		{"organizer changed to invalid churchwide scope", func(_ *AddRequest, f *AddFacts) { f.Occurrence.Organizer = OrganizerScope{ContactEvent, 99} }, false},
		{"new approved request after date removed", func(_ *AddRequest, f *AddFacts) { end := f.Now.Add(-2 * time.Hour); f.LatestAccessEndedAt = &end }, true},
		{"stale pending ask predates removal", func(_ *AddRequest, f *AddFacts) { end := f.Now.Add(-time.Minute); f.LatestAccessEndedAt = &end }, false},
		{"live date prevents duplicate completion", func(_ *AddRequest, f *AddFacts) { f.ExistingAccessActive = true }, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, f, r := addFixture()
			r.SubjectDecision, r.AdminDecision = ContactApproved, ContactApproved
			tt.change(&r, &f)
			if got := CanFinalizeAddRequest(r, f); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func accessFixture() (ContactAccount, OccurrenceAccess, OccurrenceFacts, time.Time) {
	_, f, _ := addFixture()
	a := OccurrenceAccess{SubjectUserID: 2, OccurrenceID: 4, EventID: 3, Organizer: f.Occurrence.Organizer,
		IssuerID: 1, RegistrationID: 6, RequestID: 7, Status: OccurrenceAccessActive, GrantedAt: f.Now.Add(-time.Minute)}
	return f.Subject, a, f.Occurrence, f.Now
}

func TestOccurrenceAccessCurrent(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*ContactAccount, *OccurrenceAccess, *OccurrenceFacts, *time.Time)
		want   bool
	}{
		{"recipient exact date", func(_ *ContactAccount, _ *OccurrenceAccess, _ *OccurrenceFacts, _ *time.Time) {}, true},
		{"direct visible adult provenance", func(p *ContactAccount, a *OccurrenceAccess, _ *OccurrenceFacts, _ *time.Time) {
			p.Person.Teen = false
			p.AgeYears = nil
			a.RequestID = 0
		}, true},
		{"accepted survives actual adulthood unlike contact grant", func(p *ContactAccount, _ *OccurrenceAccess, _ *OccurrenceFacts, _ *time.Time) {
			age := 18
			p.AgeYears = &age
		}, true},
		{"wrong recipient", func(p *ContactAccount, _ *OccurrenceAccess, _ *OccurrenceFacts, _ *time.Time) { p.Person.ID = 9 }, false},
		{"admin not recipient", func(p *ContactAccount, _ *OccurrenceAccess, _ *OccurrenceFacts, _ *time.Time) {
			p.Person.ID = 9
			p.Person.Admin = true
		}, false},
		{"guardian proxy not recipient", func(p *ContactAccount, _ *OccurrenceAccess, _ *OccurrenceFacts, _ *time.Time) {
			p.Person.ID = 8
			p.Person.Teen = false
		}, false},
		{"signed out", func(p *ContactAccount, _ *OccurrenceAccess, _ *OccurrenceFacts, _ *time.Time) { p.Person = nil }, false},
		{"disabled or consent lost", func(p *ContactAccount, _ *OccurrenceAccess, _ *OccurrenceFacts, _ *time.Time) { p.Active = false }, false},
		{"no completed registration", func(_ *ContactAccount, a *OccurrenceAccess, _ *OccurrenceFacts, _ *time.Time) { a.RegistrationID = 0 }, false},
		{"no issuer provenance", func(_ *ContactAccount, a *OccurrenceAccess, _ *OccurrenceFacts, _ *time.Time) { a.IssuerID = 0 }, false},
		{"sibling date or regeneration", func(_ *ContactAccount, _ *OccurrenceAccess, f *OccurrenceFacts, _ *time.Time) { f.ID++ }, false},
		{"wrong event", func(_ *ContactAccount, _ *OccurrenceAccess, f *OccurrenceFacts, _ *time.Time) { f.EventID++ }, false},
		{"organizer changed", func(_ *ContactAccount, _ *OccurrenceAccess, f *OccurrenceFacts, _ *time.Time) { f.Organizer.ID++ }, false},
		{"revoked", func(_ *ContactAccount, a *OccurrenceAccess, _ *OccurrenceFacts, now *time.Time) {
			a.Status = OccurrenceAccessEnded
			a.EndedAt = now
		}, false},
		{"reenable cannot revive tombstone", func(p *ContactAccount, a *OccurrenceAccess, _ *OccurrenceFacts, now *time.Time) {
			p.Active = true
			a.Status = OccurrenceAccessEnded
			a.EndedAt = now
		}, false},
		{"ended timestamp alone fails closed", func(_ *ContactAccount, a *OccurrenceAccess, _ *OccurrenceFacts, now *time.Time) { a.EndedAt = now }, false},
		{"pending is not access", func(_ *ContactAccount, a *OccurrenceAccess, _ *OccurrenceFacts, _ *time.Time) {
			a.Status = ContactPending
		}, false},
		{"cancelled", func(_ *ContactAccount, _ *OccurrenceAccess, f *OccurrenceFacts, _ *time.Time) { f.Scheduled = false }, false},
		{"deleted date", func(_ *ContactAccount, _ *OccurrenceAccess, f *OccurrenceFacts, _ *time.Time) { f.ID = 0 }, false},
		{"deleted event or group", func(_ *ContactAccount, _ *OccurrenceAccess, f *OccurrenceFacts, _ *time.Time) {
			f.ContextActive = false
		}, false},
		{"before grant", func(_ *ContactAccount, a *OccurrenceAccess, _ *OccurrenceFacts, now *time.Time) {
			*now = a.GrantedAt.Add(-time.Nanosecond)
		}, false},
		{"at grant", func(_ *ContactAccount, a *OccurrenceAccess, _ *OccurrenceFacts, now *time.Time) { *now = a.GrantedAt }, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, a, f, now := accessFixture()
			tt.change(&p, &a, &f, &now)
			if got := OccurrenceAccessCurrent(p, a, f, now); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestOccurrenceAccessBoundaries(t *testing.T) {
	p, a, f, now := accessFixture()
	ev, g := EventView{Visibility: InviteOnly}, Group{Status: GroupActive, Visibility: InviteOnly}
	if !CanSeeOccurrence(p, false, a, f, now) {
		t.Fatal("exact invitation missing")
	}
	if CanSeeEvent(p.Person, ev, g, Rel{}) || CanSeeGroup(p.Person, g, Rel{}) || CanSeeRoster(p.Person, g, Rel{}) ||
		CanManageEvent(p.Person, ev, Rel{}) || CanSeeContact(p.Person, false, Contact{Teen: true}) || CanSeeOccurrenceAttendees(false, true) {
		t.Fatal("invitation widened group/series/roster/contact/management/attendee scope")
	}
	if !CanSeeOccurrence(p, true, OccurrenceAccess{}, OccurrenceFacts{}, now) {
		t.Fatal("ordinary series access changed")
	}
	for _, status := range []string{"going", "maybe", "waitlisted", "declined", "removed", ""} {
		for _, reveal := range []string{VideoRevealAlways, VideoRevealRegisteredOnly} {
			want := reveal == VideoRevealAlways || status == "going"
			if got := CanSeeOccurrenceVideo(p, false, true, a, f, now, reveal, status); got != want {
				t.Fatalf("override video %s/%s = %v", reveal, status, got)
			}
		}
	}
	// Issuer role loss and a new organizer block do not enter accepted access
	// facts; pending finalization DOES fail under those same changed facts.
	_, af, r := addFixture()
	af.Manager = false
	af.Block = &ManagerAddBlock{2, f.Organizer}
	r.SubjectDecision, r.AdminDecision = ContactApproved, ContactApproved
	if CanFinalizeAddRequest(r, af) || !OccurrenceAccessCurrent(p, a, f, now) {
		t.Fatal("pending and accepted issuer lifecycle conflated")
	}
}

func TestManagerOccurrenceAddAndRecipientControls(t *testing.T) {
	s, f, r := addFixture()
	if CanDirectManagerOccurrenceAdd(s, f) {
		t.Fatal("direct teen bypass")
	}
	adultAdd(&f, &r)
	if !CanDirectManagerOccurrenceAdd(s, f) {
		t.Fatal("eligible adult direct add refused")
	}
	end := f.Now.Add(-2 * time.Hour)
	f.LatestAccessEndedAt = &end
	if CanDirectManagerOccurrenceAdd(s, f) || !CanCreateAddRequest(s, f, 0) {
		t.Fatal("ended history must require a new ask, never direct re-add")
	}
	f.Block = &ManagerAddBlock{2, f.Occurrence.Organizer}
	if CanDirectManagerOccurrenceAdd(s, f) || CanCreateAddRequest(s, f, 0) {
		t.Fatal("legacy direct add bypassed block")
	}
	p, a, _, _ := accessFixture()
	for _, tt := range []struct {
		name                string
		actor               ContactAccount
		manager, end, block bool
	}{
		{"subject", p, false, true, true},
		{"current adult manager", ContactAccount{Person: &Person{ID: 9}, Active: true}, true, true, false},
		{"old issuer no longer manager", ContactAccount{Person: &Person{ID: 1}, Active: true}, false, false, false},
		{"guardian no proxy shortcut", ContactAccount{Person: &Person{ID: 8}, Active: true}, false, false, false},
		{"inactive subject", ContactAccount{Person: p.Person}, false, false, false},
		{"teen manager", p, true, true, true}, // only own invitation
	} {
		t.Run(tt.name, func(t *testing.T) {
			if CanEndOccurrenceAccess(tt.actor, tt.manager, a) != tt.end || CanBlockManagerAdds(tt.actor, a) != tt.block {
				t.Fatal("incorrect removal/block right")
			}
			ended := a
			ended.Status = OccurrenceAccessEnded
			if CanEndOccurrenceAccess(tt.actor, tt.manager, ended) || CanBlockManagerAdds(tt.actor, ended) {
				t.Fatal("replayed ended date")
			}
		})
	}
}

func TestValidAddOptions(t *testing.T) {
	for _, tt := range []struct {
		name, purpose string
		options       AddOptions
		want          bool
	}{
		{"group default", RequestGroupAdd, AddOptions{}, true},
		{"group explicit over limit", RequestGroupAdd, AddOptions{OverLimit: true}, true},
		{"group rejects date options", RequestGroupAdd, AddOptions{AttendanceMode: "remote"}, false},
		{"date explicit over capacity", RequestOccurrenceAdd, AddOptions{AttendanceMode: "remote", OverCapacity: true}, true},
		{"date rejects group option", RequestOccurrenceAdd, AddOptions{AttendanceMode: "in_person", OverLimit: true}, false},
		{"date requires resolved mode", RequestOccurrenceAdd, AddOptions{}, false},
		{"guest limit", RequestOccurrenceAdd, AddOptions{AttendanceMode: "in_person", GuestCount: 6}, false},
		{"negative care", RequestOccurrenceAdd, AddOptions{AttendanceMode: "in_person", ChildcareCount: -1}, false},
		{"care limit", RequestOccurrenceAdd, AddOptions{AttendanceMode: "in_person", ChildcareCount: 21}, false},
		{"unicode chars not bytes", RequestOccurrenceAdd, AddOptions{AttendanceMode: "in_person", ChildcareAgesNote: strings.Repeat("é", 200)}, true},
		{"long care note", RequestOccurrenceAdd, AddOptions{AttendanceMode: "in_person", ChildcareAgesNote: strings.Repeat("é", 201)}, false},
		{"contact never add", RequestContact, AddOptions{}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidAddOptions(tt.purpose, tt.options); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestAddRequestReviewWithdrawalAndAdultDecision(t *testing.T) {
	_, f, r := addFixture()
	adultAdd(&f, &r)
	if AddRequestStatusAfterDecision(f.Subject, ContactSubject, ContactApproved, r, f) != ContactPending {
		t.Fatal("adult approval marked success without add")
	}
	if AddRequestStatusAfterDecision(f.Requester, ContactSubject, ContactApproved, r, f) != "" {
		t.Fatal("requester approved for subject")
	}
	admin := ContactAccount{Person: &Person{ID: 9, Admin: true}, Active: true}
	if AddRequestStatusAfterDecision(admin, ContactAdmin, ContactApproved, r, f) != "" {
		t.Fatal("admin bypassed adult decision")
	}
	if !CanWithdrawAddRequest(f.Requester, r, f.Now) || CanWithdrawAddRequest(f.Subject, r, f.Now) || CanWithdrawAddRequest(f.Requester, r, r.ExpiresAt) {
		t.Fatal("withdrawal authorization/expiry")
	}
	if !CanViewAddRequest(f.Subject, r) || !CanViewAddRequest(f.Requester, r) || !CanViewAddRequest(admin, r) {
		t.Fatal("safe review denied")
	}
	admin.Person.Admin = false
	if CanViewAddRequest(admin, r) {
		t.Fatal("outsider reviewed request")
	}
}

func TestGroupAddIgnoresOccurrenceBlocks(t *testing.T) {
	_, f, r := addFixture()
	r.Purpose, r.ContextType, r.OccurrenceID, r.Options = RequestGroupAdd, ContactGroup, 0, AddOptions{}
	f.ContextType = ContactGroup
	f.Block = &ManagerAddBlock{2, f.Occurrence.Organizer}
	f.LatestAccessEndedAt, f.ExistingAccessActive = &f.Now, true
	r.SubjectDecision, r.AdminDecision = ContactApproved, ContactApproved
	if !CanCreateAddRequest(r.AddScope, f, 0) || !CanFinalizeAddRequest(r, f) {
		t.Fatal("date block/history affected unrelated membership")
	}
	adultAdd(&f, &r)
	if CanCreateAddRequest(r.AddScope, f, 0) {
		t.Fatal("unrelated date history forced ordinary adult membership into an ask")
	}
}
