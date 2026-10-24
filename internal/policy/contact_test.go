package policy

import (
	"testing"
	"time"
)

func contactFixture() (ContactScope, ContactAccessFacts, ContactRequest, ContactGrant) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	age := 16
	s := ContactScope{RequesterID: 1, SubjectUserID: 2, ContextType: ContactGroup, ContextID: 3, Fields: ContactEmail}
	f := ContactAccessFacts{
		Requester: ContactAccount{Person: &Person{ID: 1}, Active: true},
		Subject:   ContactAccount{Person: &Person{ID: 2, Teen: true}, Active: true, AgeYears: &age},
		Context:   ContactContext{Type: ContactGroup, ID: 3, Active: true, GroupLeader: true, GroupMember: true},
		Now:       now,
	}
	r := ContactRequest{ContactScope: s, NeedsAdmin: true, SubjectDecision: ContactPending, AdminDecision: ContactPending,
		Status: ContactPending, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(ContactRequestLifetime)}
	g := ContactGrant{ContactScope: s, RequestID: 4, NeedsAdmin: true, GrantedAt: now.Add(-time.Minute)}
	return s, f, r, g
}

func contactAdultSubject(f *ContactAccessFacts) {
	f.Subject.Person.Teen = false
	f.Subject.AgeYears = nil
	f.Subject.HideFromLeaders = true
}

func contactEventContext(f *ContactAccessFacts) {
	end := f.Now.Add(-ContactEventGrace + time.Nanosecond)
	f.Context = ContactContext{Type: ContactEvent, ID: 3, Active: true, EventManager: true, EventRegistrant: true, LastOccurrenceEnd: &end}
}

func TestCanCreateContactRequest(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*ContactScope, *ContactAccessFacts, *int)
		want   bool
	}{
		{"teen", func(_ *ContactScope, _ *ContactAccessFacts, _ *int) {}, true},
		{"hidden adult", func(_ *ContactScope, f *ContactAccessFacts, _ *int) { contactAdultSubject(f) }, true},
		{"unhidden adult", func(_ *ContactScope, f *ContactAccessFacts, _ *int) {
			contactAdultSubject(f)
			f.Subject.HideFromLeaders = false
		}, false},
		{"email", func(s *ContactScope, _ *ContactAccessFacts, _ *int) { s.Fields = ContactEmail }, true},
		{"phone", func(s *ContactScope, _ *ContactAccessFacts, _ *int) { s.Fields = ContactPhone }, true},
		{"both", func(s *ContactScope, _ *ContactAccessFacts, _ *int) { s.Fields = ContactBoth }, true},
		{"invalid fields", func(s *ContactScope, _ *ContactAccessFacts, _ *int) { s.Fields = "address" }, false},
		{"unknown context", func(s *ContactScope, f *ContactAccessFacts, _ *int) {
			s.ContextType = "occurrence"
			f.Context.Type = s.ContextType
		}, false},
		{"zero context", func(s *ContactScope, f *ContactAccessFacts, _ *int) { s.ContextID = 0; f.Context.ID = 0 }, false},
		{"other context", func(s *ContactScope, _ *ContactAccessFacts, _ *int) { s.ContextID++ }, false},
		{"other requester", func(s *ContactScope, _ *ContactAccessFacts, _ *int) { s.RequesterID++ }, false},
		{"other subject", func(s *ContactScope, _ *ContactAccessFacts, _ *int) { s.SubjectUserID++ }, false},
		{"self", func(s *ContactScope, f *ContactAccessFacts, _ *int) {
			s.SubjectUserID = s.RequesterID
			f.Subject.Person.ID = s.RequesterID
		}, false},
		{"admin requester still eligible as leader", func(_ *ContactScope, f *ContactAccessFacts, _ *int) { f.Requester.Person.Admin = true }, true},
		{"admin not group leader", func(_ *ContactScope, f *ContactAccessFacts, _ *int) {
			f.Requester.Person.Admin = true
			f.Context.GroupLeader = false
		}, false},
		{"guardian or ordinary member is not leader", func(_ *ContactScope, f *ContactAccessFacts, _ *int) { f.Context.GroupLeader = false }, false},
		{"subject not current member", func(_ *ContactScope, f *ContactAccessFacts, _ *int) { f.Context.GroupMember = false }, false},
		{"inactive group", func(_ *ContactScope, f *ContactAccessFacts, _ *int) { f.Context.Active = false }, false},
		{"signed out", func(_ *ContactScope, f *ContactAccessFacts, _ *int) { f.Requester.Person = nil }, false},
		{"requester inactive", func(_ *ContactScope, f *ContactAccessFacts, _ *int) { f.Requester.Active = false }, false},
		{"subject inactive", func(_ *ContactScope, f *ContactAccessFacts, _ *int) { f.Subject.Active = false }, false},
		{"teen requester", func(_ *ContactScope, f *ContactAccessFacts, _ *int) { f.Requester.Person.Teen = true }, false},
		{"underage adult requester", func(_ *ContactScope, f *ContactAccessFacts, _ *int) { age := 17; f.Requester.AgeYears = &age }, false},
		{"teen unknown age", func(_ *ContactScope, f *ContactAccessFacts, _ *int) { f.Subject.AgeYears = nil }, false},
		{"stale teen already eighteen", func(_ *ContactScope, f *ContactAccessFacts, _ *int) { *f.Subject.AgeYears = 18 }, false},
		{"under thirteen", func(_ *ContactScope, f *ContactAccessFacts, _ *int) { *f.Subject.AgeYears = 12 }, false},
		{"nine open", func(_ *ContactScope, _ *ContactAccessFacts, n *int) { *n = 9 }, true},
		{"ten open", func(_ *ContactScope, _ *ContactAccessFacts, n *int) { *n = 10 }, false},
		{"negative count", func(_ *ContactScope, _ *ContactAccessFacts, n *int) { *n = -1 }, false},
		{"host actual registrant before boundary", func(s *ContactScope, f *ContactAccessFacts, _ *int) {
			contactEventContext(f)
			s.ContextType = ContactEvent
		}, true},
		{"host nonregistrant", func(s *ContactScope, f *ContactAccessFacts, _ *int) {
			contactEventContext(f)
			s.ContextType = ContactEvent
			f.Context.EventRegistrant = false
		}, false},
		{"registrant unrelated manager", func(s *ContactScope, f *ContactAccessFacts, _ *int) {
			contactEventContext(f)
			s.ContextType = ContactEvent
			f.Context.EventManager = false
		}, false},
		{"event missing last occurrence", func(s *ContactScope, f *ContactAccessFacts, _ *int) {
			contactEventContext(f)
			s.ContextType = ContactEvent
			f.Context.LastOccurrenceEnd = nil
		}, false},
		{"event exact thirty days", func(s *ContactScope, f *ContactAccessFacts, _ *int) {
			contactEventContext(f)
			s.ContextType = ContactEvent
			f.Now = f.Now.Add(time.Nanosecond)
		}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, f, _, _ := contactFixture()
			n := 0
			tt.change(&s, &f, &n)
			if got := CanCreateContactRequest(s, f, n); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestContactRequestDecisionOrders(t *testing.T) {
	for _, first := range []string{ContactSubject, ContactAdmin} {
		for _, firstDecision := range []string{ContactApproved, ContactDenied} {
			for _, secondDecision := range []string{ContactApproved, ContactDenied} {
				t.Run(first+"/"+firstDecision+"/"+secondDecision, func(t *testing.T) {
					_, f, r, _ := contactFixture()
					admin := ContactAccount{Person: &Person{ID: 1, Admin: true}, Active: true} // requester is an admin, no bypass
					actor, secondActor, second := f.Subject, admin, ContactAdmin
					if first == ContactAdmin {
						actor, secondActor, second = admin, f.Subject, ContactSubject
					}
					got := ContactRequestStatusAfterDecision(actor, first, firstDecision, r, f)
					want := ContactPending
					if firstDecision == ContactDenied {
						want = ContactDenied
					}
					if got != want {
						t.Fatalf("first = %q, want %q", got, want)
					}
					r.Status = got
					if first == ContactSubject {
						r.SubjectDecision = firstDecision
					} else {
						r.AdminDecision = firstDecision
					}
					got = ContactRequestStatusAfterDecision(secondActor, second, secondDecision, r, f)
					want = ContactGranted
					if firstDecision == ContactDenied {
						want = ""
					} else if secondDecision == ContactDenied {
						want = ContactDenied
					}
					if got != want {
						t.Fatalf("second = %q, want %q", got, want)
					}
				})
			}
		}
	}
}

func TestContactRequestAdultDecision(t *testing.T) {
	for _, decision := range []string{ContactApproved, ContactDenied} {
		t.Run(decision, func(t *testing.T) {
			_, f, r, _ := contactFixture()
			contactAdultSubject(&f)
			r.NeedsAdmin, r.AdminDecision = false, ContactNotNeeded
			want := ContactGranted
			if decision == ContactDenied {
				want = ContactDenied
			}
			if got := ContactRequestStatusAfterDecision(f.Subject, ContactSubject, decision, r, f); got != want {
				t.Fatalf("got %q want %q", got, want)
			}
			admin := ContactAccount{Person: &Person{ID: 8, Admin: true}, Active: true}
			if ContactRequestStatusAfterDecision(admin, ContactAdmin, decision, r, f) != "" {
				t.Fatal("admin decided for adult")
			}
		})
	}
}

func TestContactRequestDecisionEligibility(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*ContactAccount, *string, *ContactRequest, *ContactAccessFacts)
		want   string
	}{
		{"teen approves", func(_ *ContactAccount, _ *string, _ *ContactRequest, _ *ContactAccessFacts) {}, ContactPending},
		{"adult alone", func(a *ContactAccount, _ *string, r *ContactRequest, f *ContactAccessFacts) {
			contactAdultSubject(f)
			*a = f.Subject
			r.NeedsAdmin = false
			r.AdminDecision = ContactNotNeeded
		}, ContactGranted},
		{"guardian cannot approve", func(a *ContactAccount, _ *string, _ *ContactRequest, _ *ContactAccessFacts) {
			a.Person = &Person{ID: 8}
		}, ""},
		{"guardian cannot approve as admin", func(a *ContactAccount, p *string, _ *ContactRequest, _ *ContactAccessFacts) {
			a.Person = &Person{ID: 8}
			*p = ContactAdmin
		}, ""},
		{"inactive subject", func(a *ContactAccount, _ *string, _ *ContactRequest, _ *ContactAccessFacts) { a.Active = false }, ""},
		{"zero admin actor", func(a *ContactAccount, p *string, _ *ContactRequest, _ *ContactAccessFacts) {
			a.Person = &Person{Admin: true}
			*p = ContactAdmin
		}, ""},
		{"wrong party", func(_ *ContactAccount, p *string, _ *ContactRequest, _ *ContactAccessFacts) { *p = "guardian" }, ""},
		{"repeated approval", func(_ *ContactAccount, _ *string, r *ContactRequest, _ *ContactAccessFacts) {
			r.SubjectDecision = ContactApproved
		}, ""},
		{"before creation", func(_ *ContactAccount, _ *string, r *ContactRequest, f *ContactAccessFacts) {
			f.Now = r.CreatedAt.Add(-time.Nanosecond)
		}, ""},
		{"just before expiry", func(_ *ContactAccount, _ *string, r *ContactRequest, f *ContactAccessFacts) {
			f.Now = r.ExpiresAt.Add(-time.Nanosecond)
		}, ContactPending},
		{"exact expiry", func(_ *ContactAccount, _ *string, r *ContactRequest, f *ContactAccessFacts) { f.Now = r.ExpiresAt }, ""},
		{"age eighteen stale type", func(_ *ContactAccount, _ *string, _ *ContactRequest, f *ContactAccessFacts) { *f.Subject.AgeYears = 18 }, ""},
		{"age eighteen changed type", func(_ *ContactAccount, _ *string, _ *ContactRequest, f *ContactAccessFacts) {
			*f.Subject.AgeYears = 18
			f.Subject.Person.Teen = false
		}, ""},
		{"lost role", func(_ *ContactAccount, _ *string, _ *ContactRequest, f *ContactAccessFacts) {
			f.Context.GroupLeader = false
		}, ""},
		{"lost membership", func(_ *ContactAccount, _ *string, _ *ContactRequest, f *ContactAccessFacts) {
			f.Context.GroupMember = false
		}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, f, r, _ := contactFixture()
			actor, party := f.Subject, ContactSubject
			tt.change(&actor, &party, &r, &f)
			if got := ContactRequestStatusAfterDecision(actor, party, ContactApproved, r, f); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
	for _, status := range []string{ContactGranted, ContactDenied, ContactWithdrawn, ContactExpired, ContactEnded, "unknown"} {
		t.Run("terminal/"+status, func(t *testing.T) {
			_, f, r, _ := contactFixture()
			r.Status = status
			if ContactRequestStatusAfterDecision(f.Subject, ContactSubject, ContactApproved, r, f) != "" {
				t.Fatal("resurrected terminal request")
			}
			if CanWithdrawContactRequest(f.Requester, r, f.Now) {
				t.Fatal("withdrew terminal request")
			}
		})
	}
}

func TestContactRequestExpiryAndWithdrawal(t *testing.T) {
	_, f, r, _ := contactFixture()
	for _, tt := range []struct {
		name              string
		now               time.Time
		expired, withdraw bool
	}{
		{"before", r.ExpiresAt.Add(-time.Nanosecond), false, true},
		{"at", r.ExpiresAt, true, false},
		{"after", r.ExpiresAt.Add(time.Nanosecond), true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if ContactRequestExpired(r, tt.now) != tt.expired || CanWithdrawContactRequest(f.Requester, r, tt.now) != tt.withdraw {
				t.Fatal("incorrect expiry/withdrawal boundary")
			}
		})
	}
	if CanWithdrawContactRequest(f.Subject, r, f.Now) {
		t.Fatal("subject withdrew request")
	}
	f.Requester.Active = false
	if CanWithdrawContactRequest(f.Requester, r, f.Now) {
		t.Fatal("inactive requester withdrew request")
	}
}

func TestContactGrantCurrentAndReveal(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*ContactScope, *ContactGrant, *ContactAccessFacts)
		want   bool
	}{
		{"active", func(_ *ContactScope, _ *ContactGrant, _ *ContactAccessFacts) {}, true},
		{"both can reveal email", func(_ *ContactScope, g *ContactGrant, _ *ContactAccessFacts) { g.Fields = ContactBoth }, true},
		{"both can reveal phone", func(s *ContactScope, g *ContactGrant, _ *ContactAccessFacts) {
			g.Fields = ContactBoth
			s.Fields = ContactPhone
		}, true},
		{"email cannot reveal phone", func(s *ContactScope, _ *ContactGrant, _ *ContactAccessFacts) { s.Fields = ContactPhone }, false},
		{"email cannot reveal both", func(s *ContactScope, _ *ContactGrant, _ *ContactAccessFacts) { s.Fields = ContactBoth }, false},
		{"invalid fields", func(s *ContactScope, g *ContactGrant, _ *ContactAccessFacts) { g.Fields = ContactBoth; s.Fields = "" }, false},
		{"different context", func(s *ContactScope, _ *ContactGrant, _ *ContactAccessFacts) { s.ContextID++ }, false},
		{"different context type", func(s *ContactScope, _ *ContactGrant, _ *ContactAccessFacts) { s.ContextType = ContactEvent }, false},
		{"different subject", func(s *ContactScope, _ *ContactGrant, _ *ContactAccessFacts) { s.SubjectUserID++ }, false},
		{"different requester", func(s *ContactScope, _ *ContactGrant, _ *ContactAccessFacts) { s.RequesterID++ }, false},
		{"no provenance", func(_ *ContactScope, g *ContactGrant, _ *ContactAccessFacts) { g.RequestID = 0 }, false},
		{"revoked never resurrects", func(_ *ContactScope, g *ContactGrant, f *ContactAccessFacts) { g.EndedAt = &f.Now }, false},
		{"before grant", func(_ *ContactScope, g *ContactGrant, f *ContactAccessFacts) {
			f.Now = g.GrantedAt.Add(-time.Nanosecond)
		}, false},
		{"at grant", func(_ *ContactScope, g *ContactGrant, f *ContactAccessFacts) { f.Now = g.GrantedAt }, true},
		{"requester disabled", func(_ *ContactScope, _ *ContactGrant, f *ContactAccessFacts) { f.Requester.Active = false }, false},
		{"subject disabled", func(_ *ContactScope, _ *ContactGrant, f *ContactAccessFacts) { f.Subject.Active = false }, false},
		{"lost role", func(_ *ContactScope, _ *ContactGrant, f *ContactAccessFacts) { f.Context.GroupLeader = false }, false},
		{"lost membership", func(_ *ContactScope, _ *ContactGrant, f *ContactAccessFacts) { f.Context.GroupMember = false }, false},
		{"teen eighteenth birthday stale type", func(_ *ContactScope, _ *ContactGrant, f *ContactAccessFacts) { *f.Subject.AgeYears = 18 }, false},
		{"teen eighteenth birthday updated type", func(_ *ContactScope, _ *ContactGrant, f *ContactAccessFacts) {
			*f.Subject.AgeYears = 18
			f.Subject.Person.Teen = false
		}, false},
		{"adult grant", func(_ *ContactScope, g *ContactGrant, f *ContactAccessFacts) {
			contactAdultSubject(f)
			g.NeedsAdmin = false
		}, true},
		{"event before grace", func(s *ContactScope, g *ContactGrant, f *ContactAccessFacts) {
			contactEventContext(f)
			s.ContextType = ContactEvent
			g.ContextType = ContactEvent
		}, true},
		{"event at grace", func(s *ContactScope, g *ContactGrant, f *ContactAccessFacts) {
			contactEventContext(f)
			s.ContextType = ContactEvent
			g.ContextType = ContactEvent
			f.Now = f.Now.Add(time.Nanosecond)
		}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, f, _, g := contactFixture()
			tt.change(&s, &g, &f)
			if got := CanRevealContactGrant(f.Requester, s, g, f); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
			if CanRevealContactGrant(f.Subject, s, g, f) {
				t.Fatal("ordinary subject visibility used manager grant")
			}
		})
	}
}

func TestContactGrantRevocationReason(t *testing.T) {
	_, f, _, g := contactFixture()
	for _, tt := range []struct {
		name     string
		actor    ContactAccount
		guardian bool
		want     string
	}{
		{"subject", f.Subject, false, ContactRevokedBySubject},
		{"requester", f.Requester, false, ""},
		{"admin", ContactAccount{Person: &Person{ID: 8, Admin: true}, Active: true}, false, ContactRevokedByAdmin},
		{"confirmed guardian", ContactAccount{Person: &Person{ID: 8}, Active: true}, true, ContactRevokedByGuardian},
		{"pending or revoked guardian", ContactAccount{Person: &Person{ID: 8}, Active: true}, false, ""},
		{"inactive guardian", ContactAccount{Person: &Person{ID: 8}}, true, ""},
		{"teen guardian", ContactAccount{Person: &Person{ID: 8, Teen: true}, Active: true}, true, ""},
		{"signed out", ContactAccount{}, true, ""},
		{"zero admin actor", ContactAccount{Person: &Person{Admin: true}, Active: true}, false, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ContactGrantRevocationReason(tt.actor, tt.guardian, g); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
			ended := g
			ended.EndedAt = &f.Now
			if ContactGrantRevocationReason(tt.actor, tt.guardian, ended) != "" {
				t.Fatal("revoked twice")
			}
		})
	}
	g.NeedsAdmin = false
	if ContactGrantRevocationReason(ContactAccount{Person: &Person{ID: 8}, Active: true}, true, g) != "" {
		t.Fatal("guardian revoked adult grant")
	}
}

func TestContactIneligibility(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*ContactAccessFacts)
		want   string
	}{
		{"valid", func(_ *ContactAccessFacts) {}, ""},
		{"disabled", func(f *ContactAccessFacts) { f.Subject.Active = false }, ContactAccountDisabled},
		{"turned eighteen", func(f *ContactAccessFacts) { *f.Subject.AgeYears = 18 }, ContactTurned18},
		{"lost role", func(f *ContactAccessFacts) { f.Context.GroupLeader = false }, ContactLeftRole},
		{"left subject", func(f *ContactAccessFacts) { f.Context.GroupMember = false }, ContactLeftContext},
		{"context deleted", func(f *ContactAccessFacts) { f.Context.Active = false }, ContactContextEnded},
		{"event ended", func(f *ContactAccessFacts) { contactEventContext(f); f.Now = f.Now.Add(time.Nanosecond) }, ContactEventEnded},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, f, _, _ := contactFixture()
			tt.change(&f)
			if got := ContactIneligibility(f, true); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}
