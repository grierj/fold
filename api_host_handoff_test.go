package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/notify"
	"fold/models"

	"gorm.io/gorm"
)

// Church-wide event hosts (M7.4, R4-H). The barrier tests below hold a
// test-owned transaction at a named statement (pauseOnStatement, holdAdvisory),
// start the competing real request, and read from pg_locks which relation it
// waits on. Nothing sleeps and no production hook exists.

type httpResult struct {
	code int
	body string
}

// bgDo sends one request on its own goroutine. Give each concurrent request
// its own client: a client keeps one cookie jar.
func bgDo(c *apiClient, method, path string, body any) <-chan httpResult {
	ch := make(chan httpResult, 1)
	go func() {
		w := c.do(method, path, body, true)
		ch <- httpResult{w.Code, w.Body.String()}
	}()
	return ch
}

func awaitResult(t *testing.T, ch <-chan httpResult) httpResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("request never finished")
		return httpResult{}
	}
}

// waitsOnRelation waits until the backend holds exactly one tuple lock in
// pg_locks, on the named relation. A backend that waits for a row lock holds
// the tuple lock of the row it waits for, so this names what it is stuck on.
func waitsOnRelation(t *testing.T, db *gorm.DB, pid int, want string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		var got []string
		if err := db.Raw(`SELECT c.relname FROM pg_locks l JOIN pg_class c ON c.oid = l.relation
			WHERE l.pid = ? AND l.locktype = 'tuple' ORDER BY c.relname`, pid).Scan(&got).Error; err != nil {
			t.Fatal(err)
		}
		if len(got) == 1 && got[0] == want {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatalf("session %d holds tuple locks on %v, want only %q", pid, got, want)
		}
	}
}

var hostBarrierKey atomic.Int64

// nextHostKey is a fresh advisory key for one pause in this binary.
func nextHostKey() int64 { return 7403100 + hostBarrierKey.Add(1) }

const hostPW = "password1234"

// hostScene: admin A (acts), admin B (the competing admin), and the target, an
// active adult. Their ids ascend in that order.
type hostScene struct {
	app            *App
	adminA, adminB *models.User
	target         *models.User
	a, b           *apiClient
}

func newHostScene(t *testing.T) hostScene {
	t.Helper()
	app := testApp(t)
	s := hostScene{app: app}
	s.adminA = newTestUser(t, app, "admin", hostPW, models.RoleAdmin)
	s.adminB = newTestUser(t, app, "admin2", hostPW, models.RoleAdmin)
	s.target = newTestUser(t, app, "target", hostPW, models.RoleMember)
	s.a = loginAPI(t, app, "admin", hostPW)
	s.b = loginAPI(t, app, "admin2", hostPW)
	return s
}

// churchEvent creates a church-wide event as c (who becomes its host) with the
// listed other hosts.
func churchEvent(t *testing.T, c *apiClient, title string, hosts ...string) eventJSON {
	t.Helper()
	return churchEventAt(t, c, title, feedStart(20), hosts...)
}

func churchEventAt(t *testing.T, c *apiClient, title string, start time.Time, hosts ...string) eventJSON {
	t.Helper()
	w := c.do("POST", "/api/v1/events", map[string]any{
		"title": title, "startsLocal": start.Format("2006-01-02T15:04"),
		"durationMinutes": 60, "tzid": "UTC", "hosts": hosts,
	}, true)
	if w.Code != http.StatusCreated {
		t.Fatalf("create church event %q: %d %s", title, w.Code, w.Body.String())
	}
	var created struct {
		Event eventJSON `json:"event"`
	}
	decodeJSON(t, w, &created)
	return created.Event
}

// tombstoneHost leaves a host row for an account that was deleted, the way
// erasure did before it was refused: the row names a deleted user.
func tombstoneHost(t *testing.T, app *App, eventID, addedBy uint, name string) *models.User {
	t.Helper()
	u := newTestUser(t, app, name, hostPW, models.RoleMember)
	if err := app.DB.Exec("INSERT INTO event_hosts (event_id, user_id, added_by, created_at) VALUES (?, ?, ?, now())",
		eventID, u.ID, addedBy).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Exec("UPDATE users SET deleted_at = now() WHERE id = ?", u.ID).Error; err != nil {
		t.Fatal(err)
	}
	return u
}

// hostRowUsers is the user ids on an event's host rows, ascending, deleted
// accounts included.
func hostRowUsers(t *testing.T, db *gorm.DB, eventID uint) []uint {
	t.Helper()
	var ids []uint
	if err := db.Table("event_hosts").Where("event_id = ?", eventID).Order("user_id").Pluck("user_id", &ids).Error; err != nil {
		t.Fatal(err)
	}
	return ids
}

func sameIDs(got []uint, want ...uint) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// The last host leaves only when another live (or disabled) host remains.
func TestRemoveEventHostCountsOnlyLiveHosts(t *testing.T) {
	t.Run("a tombstone is not a co-host", func(t *testing.T) {
		s := newHostScene(t)
		ev := churchEvent(t, s.a, "Tombstone co-host")
		tomb := tombstoneHost(t, s.app, ev.ID, s.adminA.ID, "gone")
		w := s.a.do("DELETE", fmt.Sprintf("/api/v1/events/%d/hosts/%d", ev.ID, s.adminA.ID), nil, true)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"needs_host"`) {
			t.Fatalf("remove the only live host: %d %s", w.Code, w.Body.String())
		}
		if got := hostRowUsers(t, s.app.DB, ev.ID); !sameIDs(got, s.adminA.ID, tomb.ID) {
			t.Fatalf("host rows after a refused removal: %v", got)
		}
	})
	t.Run("a disabled co-host keeps the event hosted", func(t *testing.T) {
		s := newHostScene(t)
		ev := churchEvent(t, s.a, "Disabled co-host", s.target.Email)
		apiDo(t, s.b, "PUT", "/api/v1/admin/users/"+itoa(s.target.ID), map[string]any{"status": "disabled"}, http.StatusOK)
		apiDo(t, s.a, "DELETE", fmt.Sprintf("/api/v1/events/%d/hosts/%d", ev.ID, s.adminA.ID), nil, http.StatusOK)
		if got := hostRowUsers(t, s.app.DB, ev.ID); !sameIDs(got, s.target.ID) {
			t.Fatalf("host rows: %v, want only the disabled co-host", got)
		}
	})
	t.Run("a tombstone row can go while a live host remains", func(t *testing.T) {
		s := newHostScene(t)
		ev := churchEvent(t, s.a, "Remove tombstone")
		tomb := tombstoneHost(t, s.app, ev.ID, s.adminA.ID, "gone")
		apiDo(t, s.a, "DELETE", fmt.Sprintf("/api/v1/events/%d/hosts/%d", ev.ID, tomb.ID), nil, http.StatusOK)
		if got := hostRowUsers(t, s.app.DB, ev.ID); !sameIDs(got, s.adminA.ID) {
			t.Fatalf("host rows: %v", got)
		}
		rows, _ := auditRows(t, s.app, audit.EventHostRemoved)
		var meta map[string]uint
		if len(rows) != 1 || json.Unmarshal([]byte(rows[0].MetaJSON), &meta) != nil || len(meta) != 1 || meta["userId"] != tomb.ID {
			t.Fatalf("removal audit: %+v", rows)
		}
	})
	t.Run("an admin demoted before the removal gets 401 and nothing changes", func(t *testing.T) {
		s := newHostScene(t)
		ev := churchEvent(t, s.a, "Demoted remover", s.target.Email)
		// The removal waits for the contact gate; the demotion commits meanwhile.
		release, gPID := holdAdvisory(t, s.app.DB, contactLockID)
		removal := bgDo(s.a, "DELETE", fmt.Sprintf("/api/v1/events/%d/hosts/%d", ev.ID, s.target.ID), nil)
		waiterOn(t, s.app.DB, gPID)
		if err := s.app.DB.Exec("UPDATE users SET role = ?, session_version = session_version + 1 WHERE id = ?",
			models.RoleMember, s.adminA.ID).Error; err != nil {
			t.Fatal(err)
		}
		release()
		r := awaitResult(t, removal)
		if r.code != http.StatusUnauthorized || !strings.Contains(r.body, `"code":"signed_out"`) {
			t.Fatalf("removal by a demoted admin: %d %s", r.code, r.body)
		}
		if got := hostRowUsers(t, s.app.DB, ev.ID); !sameIDs(got, s.adminA.ID, s.target.ID) {
			t.Fatalf("host rows: %v", got)
		}
		if rows, _ := auditRows(t, s.app, audit.EventHostRemoved); len(rows) != 0 {
			t.Fatalf("a refused removal was audited: %+v", rows)
		}
	})
	t.Run("a deleted event answers 404", func(t *testing.T) {
		s := newHostScene(t)
		ev := churchEvent(t, s.a, "Gone event", s.target.Email)
		release, gPID := holdAdvisory(t, s.app.DB, contactLockID)
		removal := bgDo(s.a, "DELETE", fmt.Sprintf("/api/v1/events/%d/hosts/%d", ev.ID, s.target.ID), nil)
		waiterOn(t, s.app.DB, gPID)
		if err := s.app.DB.Exec("UPDATE events SET deleted_at = now() WHERE id = ?", ev.ID).Error; err != nil {
			t.Fatal(err)
		}
		release()
		r := awaitResult(t, removal)
		if r.code != http.StatusNotFound || !strings.Contains(r.body, `"code":"not_found"`) {
			t.Fatalf("removal on a deleted event: %d %s", r.code, r.body)
		}
	})
}

// A leftover host row of a deleted account is cleaned up, with an audit row,
// the next time the event gets a live host.
func TestAddEventHostRepairsTombstones(t *testing.T) {
	t.Run("tombstone-only event", func(t *testing.T) {
		s := newHostScene(t)
		ev := churchEvent(t, s.a, "Hostless")
		tomb := tombstoneHost(t, s.app, ev.ID, s.adminA.ID, "gone")
		if err := s.app.DB.Exec("DELETE FROM event_hosts WHERE event_id = ? AND user_id = ?", ev.ID, s.adminA.ID).Error; err != nil {
			t.Fatal(err)
		}
		if page, _ := getEvent(t, s.a, ev.ID, http.StatusOK); len(page.Event.Hosts) != 0 {
			t.Fatalf("the host list shows %+v for a tombstone", page.Event.Hosts)
		}
		w := s.a.do("DELETE", fmt.Sprintf("/api/v1/events/%d/hosts/%d", ev.ID, tomb.ID), nil, true)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"needs_host"`) {
			t.Fatalf("remove the tombstone: %d %s", w.Code, w.Body.String())
		}
		apiDo(t, s.a, "POST", fmt.Sprintf("/api/v1/events/%d/hosts", ev.ID), map[string]any{"email": s.target.Email}, http.StatusOK)
		if got := hostRowUsers(t, s.app.DB, ev.ID); !sameIDs(got, s.target.ID) {
			t.Fatalf("host rows after the repair: %v", got)
		}
		rows, metas := auditRows(t, s.app, audit.EventHostRemoved)
		if len(rows) != 1 || rows[0].TargetID != ev.ID || rows[0].ActorID == nil || *rows[0].ActorID != s.adminA.ID ||
			metas[0].UserID != tomb.ID || metas[0].Reason != "deleted_host_cleanup" {
			t.Fatalf("cleanup audit: %+v %+v", rows, metas)
		}
		if added, _ := auditRows(t, s.app, audit.EventHostAdded); len(added) != 1 {
			t.Fatalf("host_added audits: %d, want 1", len(added))
		}
	})
	t.Run("adding the live host again repairs without an add audit", func(t *testing.T) {
		s := newHostScene(t)
		ev := churchEvent(t, s.a, "Live and gone")
		tomb := tombstoneHost(t, s.app, ev.ID, s.adminA.ID, "gone")
		apiDo(t, s.a, "POST", fmt.Sprintf("/api/v1/events/%d/hosts", ev.ID), map[string]any{"email": s.adminA.Email}, http.StatusOK)
		if got := hostRowUsers(t, s.app.DB, ev.ID); !sameIDs(got, s.adminA.ID) {
			t.Fatalf("host rows: %v", got)
		}
		if added, _ := auditRows(t, s.app, audit.EventHostAdded); len(added) != 0 {
			t.Fatalf("host_added audits: %d, want none", len(added))
		}
		rows, metas := auditRows(t, s.app, audit.EventHostRemoved)
		if len(rows) != 1 || metas[0].UserID != tomb.ID || metas[0].Reason != "deleted_host_cleanup" {
			t.Fatalf("cleanup audit: %+v %+v", rows, metas)
		}
	})
	t.Run("two concurrent adds of one person", func(t *testing.T) {
		s := newHostScene(t)
		ev := churchEvent(t, s.a, "Double add")
		path := fmt.Sprintf("/api/v1/events/%d/hosts", ev.ID)
		body := map[string]any{"email": s.target.Email}
		key := nextHostKey()
		pauseOnStatement(t, s.app.DB, "AFTER INSERT", "event_hosts", key)
		release, hPID := holdAdvisory(t, s.app.DB, key)
		first := bgDo(s.a, "POST", path, body)
		firstPID := waiterOn(t, s.app.DB, hPID) // it wrote its row and stopped
		second := bgDo(s.b, "POST", path, body)
		waiterOn(t, s.app.DB, firstPID) // the second insert waits for the first row
		release()
		for _, r := range []httpResult{awaitResult(t, first), awaitResult(t, second)} {
			if r.code != http.StatusOK {
				t.Fatalf("add: %d %s", r.code, r.body)
			}
		}
		if got := hostRowUsers(t, s.app.DB, ev.ID); !sameIDs(got, s.adminA.ID, s.target.ID) {
			t.Fatalf("host rows: %v", got)
		}
		if added, _ := auditRows(t, s.app, audit.EventHostAdded); len(added) != 1 {
			t.Fatalf("host_added audits: %d, want 1", len(added))
		}
	})
}

// hostWriter is one of the two ungated entries that put a user on an event:
// adding a host and creating an event that lists one.
type hostWriter struct {
	name string
	// prepare makes the event (if the writer needs one) and returns the request
	// and a count of everything the write leaves behind.
	prepare func(t *testing.T, s hostScene) (method, path string, body any, wrote func() int64)
}

func hostWriters() []hostWriter {
	count := func(s hostScene, table, where string, args ...any) int64 {
		var n int64
		if err := s.app.DB.Table(table).Where(where, args...).Count(&n).Error; err != nil {
			panic(err)
		}
		return n
	}
	return []hostWriter{
		{"add host", func(t *testing.T, s hostScene) (string, string, any, func() int64) {
			ev := churchEvent(t, s.a, "Barrier add")
			return "POST", fmt.Sprintf("/api/v1/events/%d/hosts", ev.ID), map[string]any{"email": s.target.Email},
				func() int64 {
					return count(s, "event_hosts", "event_id = ? AND user_id = ?", ev.ID, s.target.ID) +
						count(s, "audit_logs", "action = ?", audit.EventHostAdded)
				}
		}},
		{"create event", func(t *testing.T, s hostScene) (string, string, any, func() int64) {
			return "POST", "/api/v1/events", map[string]any{
				"title": "Barrier create", "startsLocal": feedStart(20).Format("2006-01-02T15:04"),
				"durationMinutes": 60, "tzid": "UTC", "hosts": []string{s.target.Email},
			}, func() int64 {
				return count(s, "events", "title = ?", "Barrier create") +
					count(s, "event_occurrences", "event_id IN (SELECT id FROM events WHERE title = ?)", "Barrier create") +
					count(s, "audit_logs", "action = ?", audit.EventCreated)
			}
		}},
	}
}

// With the writer first, its user locks hold the competing change back until
// the write commits; with the competitor first, the writer rechecks the locked
// row and writes nothing.
func TestHostWritesSerializeWithAccountChanges(t *testing.T) {
	for _, w := range hostWriters() {
		t.Run(w.name+"/writer first", func(t *testing.T) {
			rivals := []struct {
				name    string
				request func(s hostScene) (method, path string, body any)
				check   func(t *testing.T, s hostScene, r httpResult)
			}{
				{"disable the target", func(s hostScene) (string, string, any) {
					return "PUT", "/api/v1/admin/users/" + itoa(s.target.ID), map[string]any{"status": "disabled"}
				}, func(t *testing.T, s hostScene, r httpResult) {
					if r.code != http.StatusOK {
						t.Fatalf("disable: %d %s", r.code, r.body)
					}
					// The target stays a host row of the disabled account (DEC-38).
					var u models.User
					if err := s.app.DB.First(&u, s.target.ID).Error; err != nil || u.Status != models.UserDisabled {
						t.Fatalf("target after the disable: %+v %v", u, err)
					}
				}},
				{"demote the acting admin", func(s hostScene) (string, string, any) {
					return "PUT", "/api/v1/admin/users/" + itoa(s.adminA.ID), map[string]any{"role": models.RoleMember}
				}, func(t *testing.T, s hostScene, r httpResult) {
					if r.code != http.StatusOK {
						t.Fatalf("demote: %d %s", r.code, r.body)
					}
				}},
				{"delete the target", func(s hostScene) (string, string, any) {
					return "DELETE", "/api/v1/admin/users/" + itoa(s.target.ID), nil
				}, func(t *testing.T, s hostScene, r httpResult) {
					// The target became a host after the delete read its events and
					// before it locked the account: the set moved, so the delete
					// answers with the current list and changes nothing. Sending the
					// same request again finds the target a co-host, so it goes.
					if r.code != http.StatusBadRequest || !strings.Contains(r.body, `"code":"needs_host_successor"`) {
						t.Fatalf("delete after the set moved: %d %s", r.code, r.body)
					}
					var u models.User
					if err := s.app.DB.First(&u, s.target.ID).Error; err != nil {
						t.Fatalf("the refused delete removed the target: %v", err)
					}
					if again := deleteAccount(s.b, s.target.ID, nil); again.code != http.StatusOK {
						t.Fatalf("delete again: %d %s", again.code, again.body)
					}
					if n := countRows(t, s.app.DB, "event_hosts", "user_id = ?", s.target.ID); n != 0 {
						t.Fatalf("%d host rows left for the deleted target", n)
					}
				}},
			}
			for _, rival := range rivals {
				t.Run(rival.name, func(t *testing.T) {
					s := newHostScene(t)
					method, path, body, wrote := w.prepare(t, s)
					db := s.app.DB
					key := nextHostKey()
					pauseOnStatement(t, db, "AFTER INSERT", "event_hosts", key)
					release, hPID := holdAdvisory(t, db, key)
					write := bgDo(s.a, method, path, body)
					writerPID := waiterOn(t, db, hPID) // it holds its user locks and has inserted a host row
					m, p, b := rival.request(s)
					rivalCh := bgDo(s.b, m, p, b)
					rivalPID := waiterOn(t, db, writerPID)
					waitsOnRelation(t, db, rivalPID, "users")
					release()
					if r := awaitResult(t, write); r.code != http.StatusOK && r.code != http.StatusCreated {
						t.Fatalf("%s: %d %s", w.name, r.code, r.body)
					}
					rival.check(t, s, awaitResult(t, rivalCh))
					if wrote() == 0 {
						t.Fatalf("%s left nothing behind", w.name)
					}
				})
			}
		})
		t.Run(w.name+"/competitor first", func(t *testing.T) {
			racers := []struct {
				name     string
				table    string // the statement the competitor stops at
				when     string
				request  func(s hostScene) (method, path string, body any)
				wantCode int
				wantBody string
			}{
				{"disable the target", "audit_logs", "AFTER INSERT", func(s hostScene) (string, string, any) {
					return "PUT", "/api/v1/admin/users/" + itoa(s.target.ID), map[string]any{"status": "disabled"}
				}, http.StatusBadRequest, `"code":"cannot_host"`},
				{"demote the acting admin", "audit_logs", "AFTER INSERT", func(s hostScene) (string, string, any) {
					return "PUT", "/api/v1/admin/users/" + itoa(s.adminA.ID), map[string]any{"role": models.RoleMember}
				}, http.StatusUnauthorized, `"code":"signed_out"`},
				{"delete the target", "notifications", "BEFORE DELETE", func(s hostScene) (string, string, any) {
					return "DELETE", "/api/v1/admin/users/" + itoa(s.target.ID), nil
				}, http.StatusNotFound, `"code":"no_account"`},
			}
			for _, racer := range racers {
				t.Run(racer.name, func(t *testing.T) {
					s := newHostScene(t)
					method, path, body, wrote := w.prepare(t, s)
					db := s.app.DB
					key := nextHostKey()
					pauseOnStatement(t, db, racer.when, racer.table, key)
					release, hPID := holdAdvisory(t, db, key)
					m, p, b := racer.request(s)
					rivalCh := bgDo(s.b, m, p, b)
					rivalPID := waiterOn(t, db, hPID) // it holds the account row it is changing
					write := bgDo(s.a, method, path, body)
					writerPID := waiterOn(t, db, rivalPID)
					waitsOnRelation(t, db, writerPID, "users")
					release()
					if r := awaitResult(t, rivalCh); r.code != http.StatusOK {
						t.Fatalf("%s: %d %s", racer.name, r.code, r.body)
					}
					r := awaitResult(t, write)
					if r.code != racer.wantCode || !strings.Contains(r.body, racer.wantBody) {
						t.Fatalf("%s after %s: %d %s, want %d %s", w.name, racer.name, r.code, r.body, racer.wantCode, racer.wantBody)
					}
					if n := wrote(); n != 0 {
						t.Fatalf("%s wrote %d rows after it was refused", w.name, n)
					}
				})
			}
		})
	}
}

// Event deletion locks its events FOR UPDATE before any child row goes, so an
// add of a host either lands first and is deleted with the event, or waits and
// finds the event gone.
func TestEventDeleteSerializesWithAddHost(t *testing.T) {
	t.Run("add first", func(t *testing.T) {
		s := newHostScene(t)
		ev := churchEvent(t, s.a, "Add then delete")
		db := s.app.DB
		key := nextHostKey()
		pauseOnStatement(t, db, "AFTER INSERT", "event_hosts", key)
		release, hPID := holdAdvisory(t, db, key)
		add := bgDo(s.a, "POST", fmt.Sprintf("/api/v1/events/%d/hosts", ev.ID), map[string]any{"email": s.target.Email})
		addPID := waiterOn(t, db, hPID)
		del := bgDo(s.b, "DELETE", fmt.Sprintf("/api/v1/events/%d", ev.ID), nil)
		delPID := waiterOn(t, db, addPID)
		waitsOnRelation(t, db, delPID, "events")
		release()
		if r := awaitResult(t, add); r.code != http.StatusOK {
			t.Fatalf("add: %d %s", r.code, r.body)
		}
		if r := awaitResult(t, del); r.code != http.StatusOK {
			t.Fatalf("delete: %d %s", r.code, r.body)
		}
		if got := hostRowUsers(t, db, ev.ID); len(got) != 0 {
			t.Fatalf("host rows left on the deleted event: %v", got)
		}
	})
	t.Run("delete first", func(t *testing.T) {
		s := newHostScene(t)
		ev := churchEvent(t, s.a, "Delete then add")
		db := s.app.DB
		key := nextHostKey()
		pauseOnStatement(t, db, "BEFORE DELETE", "event_occurrences", key)
		release, hPID := holdAdvisory(t, db, key)
		del := bgDo(s.b, "DELETE", fmt.Sprintf("/api/v1/events/%d", ev.ID), nil)
		delPID := waiterOn(t, db, hPID) // it holds the event FOR UPDATE and has not touched a child row
		add := bgDo(s.a, "POST", fmt.Sprintf("/api/v1/events/%d/hosts", ev.ID), map[string]any{"email": s.target.Email})
		addPID := waiterOn(t, db, delPID)
		waitsOnRelation(t, db, addPID, "events")
		release()
		if r := awaitResult(t, del); r.code != http.StatusOK {
			t.Fatalf("delete: %d %s", r.code, r.body)
		}
		r := awaitResult(t, add)
		if r.code != http.StatusNotFound || !strings.Contains(r.body, `"code":"not_found"`) {
			t.Fatalf("add after the delete: %d %s", r.code, r.body)
		}
		if got := hostRowUsers(t, db, ev.ID); len(got) != 0 {
			t.Fatalf("host rows on the deleted event: %v", got)
		}
	})
}

// onlyHosts replaces an event's host rows with exactly these users.
func onlyHosts(t *testing.T, db *gorm.DB, eventID, addedBy uint, users ...uint) {
	t.Helper()
	if err := db.Exec("DELETE FROM event_hosts WHERE event_id = ?", eventID).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range users {
		if err := db.Exec("INSERT INTO event_hosts (event_id, user_id, added_by, created_at) VALUES (?, ?, ?, now())",
			eventID, id, addedBy).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// hostSeatsByEvent lists the live church-wide events a user hosts with every
// seat, deleted accounts marked; soleHosted picks the ones they cannot leave.
func TestHostSeatsAndSoleHosted(t *testing.T) {
	s := newHostScene(t)
	db := s.app.DB
	a, tgt := s.adminA.ID, s.target.ID

	shared := churchEvent(t, s.a, "Shared", s.target.Email) // {A, target}
	churchEvent(t, s.a, "Not hosted")                       // {A}
	alone := churchEvent(t, s.a, "Alone")
	onlyHosts(t, db, alone.ID, a, tgt) // {target}
	withTomb := churchEvent(t, s.a, "With tombstone")
	tomb := tombstoneHost(t, s.app, withTomb.ID, a, "gone")
	onlyHosts(t, db, withTomb.ID, a, tgt, tomb.ID) // {target, tombstone}
	deleted := churchEvent(t, s.a, "Deleted event")
	onlyHosts(t, db, deleted.ID, a, tgt)
	if err := db.Exec("UPDATE events SET deleted_at = now() WHERE id = ?", deleted.ID).Error; err != nil {
		t.Fatal(err)
	}
	g := activeGroup(t, s.app, s.adminA, "Group hosts", models.VisibilityPublic, "")
	groupEvent, _ := seedEvent(t, s.app, &g, a, feedStart(9))
	onlyHosts(t, db, groupEvent.ID, a, tgt)

	seats, err := hostSeatsByEvent(db, tgt)
	if err != nil {
		t.Fatal(err)
	}
	if len(seats) != 3 || len(seats[shared.ID]) != 2 || len(seats[alone.ID]) != 1 || len(seats[withTomb.ID]) != 2 {
		t.Fatalf("seats: %+v", seats)
	}
	if got := seats[withTomb.ID]; got[0].UserID != tgt || got[0].Deleted || got[1].UserID != tomb.ID || !got[1].Deleted {
		t.Fatalf("seats of the event with a tombstone: %+v", got)
	}
	if got := soleHosted(seats, tgt); !sameIDs(got, alone.ID, withTomb.ID) {
		t.Fatalf("soleHosted = %v, want [%d %d]", got, alone.ID, withTomb.ID)
	}
	if len(soleHosted(nil, tgt)) != 0 {
		t.Fatal("no seats, no sole-hosted events")
	}
}

// ---------------------------------------------------------------------------
// Replacement host before account deletion (M7.4, R4-H, owner decision 3)
// ---------------------------------------------------------------------------

func countRows(t *testing.T, db *gorm.DB, table, where string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.Table(table).Where(where, args...).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func deleteAccount(c *apiClient, id uint, body any) httpResult {
	w := c.do("DELETE", "/api/v1/admin/users/"+itoa(id), body, true)
	return httpResult{w.Code, w.Body.String()}
}

// withHostPicks is a delete body naming a replacement per event id.
func withHostPicks(picks map[uint]uint) map[string]any {
	m := map[string]uint{}
	for ev, user := range picks {
		m[itoa(ev)] = user
	}
	return map[string]any{"hostSuccessors": m}
}

// soleHostEvent is a church-wide event the scene's target hosts alone.
func soleHostEvent(t *testing.T, s hostScene, title string) eventJSON {
	t.Helper()
	ev := churchEvent(t, s.a, title)
	onlyHosts(t, s.app.DB, ev.ID, s.adminA.ID, s.target.ID)
	return ev
}

func extraUser(t *testing.T, s hostScene, name string) *models.User {
	t.Helper()
	return newTestUser(t, s.app, name, hostPW, models.RoleMember)
}

// accountState is everything a refused deletion must leave alone, as one string.
func accountState(t *testing.T, db *gorm.DB, uid uint) string {
	t.Helper()
	var st struct {
		Hosts, OwnHosts, Memberships, Tokens, Notices, Audits, Live, Epoch int64
		Email                                                              string
	}
	err := db.Raw(`SELECT
		(SELECT count(*) FROM event_hosts) AS hosts,
		(SELECT count(*) FROM event_hosts WHERE user_id = ?) AS own_hosts,
		(SELECT count(*) FROM memberships WHERE user_id = ?) AS memberships,
		(SELECT count(*) FROM account_tokens WHERE user_id = ?) AS tokens,
		(SELECT count(*) FROM notifications WHERE user_id = ?) AS notices,
		(SELECT count(*) FROM audit_logs) AS audits,
		(SELECT count(*) FROM users WHERE id = ? AND deleted_at IS NULL) AS live,
		(SELECT session_version FROM users WHERE id = ?) AS epoch,
		(SELECT email FROM users WHERE id = ?) AS email`, uid, uid, uid, uid, uid, uid, uid).Scan(&st).Error
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%+v", st)
}

func decodeNeedHosts(t *testing.T, body string) soleHostEventsJSON {
	t.Helper()
	var out soleHostEventsJSON
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return out
}

func eventIDs(out soleHostEventsJSON) []uint {
	ids := make([]uint, 0, len(out.Events))
	for _, e := range out.Events {
		ids = append(ids, e.ID)
	}
	return ids
}

func candidateIDs(out soleHostEventsJSON) []uint {
	ids := make([]uint, 0, len(out.Candidates))
	for _, c := range out.Candidates {
		ids = append(ids, c.ID)
	}
	return ids
}

// waitsOnGate waits until the backend waits for the contact advisory lock.
func waitsOnGate(t *testing.T, db *gorm.DB, pid int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		var n int64
		if err := db.Raw(`SELECT count(*) FROM pg_locks WHERE pid = ? AND locktype = 'advisory' AND NOT granted
			AND ((classid::bigint << 32) | objid::bigint) = ?`, pid, contactLockID).Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n == 1 {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatalf("session %d never waited for the contact gate", pid)
		}
	}
}

func rawDelete(c *apiClient, path, raw string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("DELETE", path, strings.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	for _, ck := range c.jar {
		req.AddCookie(ck)
	}
	req.Header.Set("X-CSRF-Token", c.token)
	w := httptest.NewRecorder()
	c.router.ServeHTTP(w, req)
	return w
}

func soleHostEventsGET(t *testing.T, c *apiClient, uid uint) (int, soleHostEventsJSON, string) {
	t.Helper()
	w := c.do("GET", "/api/v1/admin/users/"+itoa(uid)+"/sole-host-events", nil, false)
	var out soleHostEventsJSON
	if w.Code == http.StatusOK {
		decodeJSON(t, w, &out)
	}
	return w.Code, out, w.Body.String()
}

// The discovery endpoint lists the church-wide events the person hosts alone,
// whether or not a deleted account still has a row on them.
func TestSoleHostEventsDiscovery(t *testing.T) {
	s := newHostScene(t)
	db := s.app.DB
	a, tgt := s.adminA.ID, s.target.ID
	ann := extraUser(t, s, "ann")
	bob := extraUser(t, s, "bob")
	dee := extraUser(t, s, "dee")
	tina := extraUser(t, s, "tina")
	ivy := extraUser(t, s, "ivy")
	gone := extraUser(t, s, "gone")
	if err := db.Model(dee).Update("status", models.UserDisabled).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(tina).Updates(map[string]any{"account_type": models.AccountTeen, "birth_date": time.Now().AddDate(-15, 0, 0)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(ivy).Update("status", models.UserInvited).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE users SET deleted_at = now() WHERE id = ?", gone.ID).Error; err != nil {
		t.Fatal(err)
	}

	sole := soleHostEvent(t, s, "Sole")
	tombCo := churchEvent(t, s.a, "Only a tombstone beside")
	tomb := tombstoneHost(t, s.app, tombCo.ID, a, "ghost")
	onlyHosts(t, db, tombCo.ID, a, tgt, tomb.ID)
	past := soleHostEvent(t, s, "Past only")
	if err := db.Exec(`UPDATE event_occurrences SET starts_at = now() - interval '3 days',
		ends_at = now() - interval '3 days' + interval '1 hour' WHERE event_id = ?`, past.ID).Error; err != nil {
		t.Fatal(err)
	}
	live := churchEvent(t, s.a, "Live co-host", s.target.Email) // {A, target}
	disabledCo := churchEvent(t, s.a, "Disabled co-host")
	onlyHosts(t, db, disabledCo.ID, a, tgt, dee.ID)
	notMine := churchEvent(t, s.a, "Not hosted by the target")
	tombOnly := churchEvent(t, s.a, "Tombstone only")
	tomb2 := tombstoneHost(t, s.app, tombOnly.ID, a, "ghost2")
	onlyHosts(t, db, tombOnly.ID, a, tomb2.ID)
	deleted := soleHostEvent(t, s, "Deleted event")
	if err := db.Exec("UPDATE events SET deleted_at = now() WHERE id = ?", deleted.ID).Error; err != nil {
		t.Fatal(err)
	}
	g := activeGroup(t, s.app, s.adminA, "Group hosts", models.VisibilityPublic, "")
	groupEvent, _ := seedEvent(t, s.app, &g, a, feedStart(9))
	onlyHosts(t, db, groupEvent.ID, a, tgt)
	_, _ = live, notMine

	code, out, body := soleHostEventsGET(t, s.a, tgt)
	if code != http.StatusOK {
		t.Fatalf("discovery: %d %s", code, body)
	}
	titles := map[string]soleHostEventJSON{}
	for _, e := range out.Events {
		titles[e.Title] = e
	}
	if len(out.Events) != 3 || titles["Sole"].ID != sole.ID || titles["Only a tombstone beside"].ID != tombCo.ID || titles["Past only"].ID != past.ID {
		t.Fatalf("sole-host events: %+v", out.Events)
	}
	if titles["Sole"].NextStartsAt == nil || titles["Only a tombstone beside"].NextStartsAt == nil {
		t.Fatalf("an event with a coming date has no nextStartsAt: %+v", out.Events)
	}
	if titles["Past only"].NextStartsAt != nil {
		t.Fatalf("a past-only event has nextStartsAt: %+v", titles["Past only"])
	}
	if out.Events[0].Title != "Only a tombstone beside" || out.Events[1].Title != "Past only" || out.Events[2].Title != "Sole" {
		t.Fatalf("events are not ordered by title: %+v", out.Events)
	}
	// Active adults only: not the target, a teen, a disabled, invited or
	// deleted account. The acting admin is a candidate; ordered by name.
	if got := candidateIDs(out); !sameIDs(got, a, s.adminB.ID, ann.ID, bob.ID) {
		t.Fatalf("candidates = %v, want [%d %d %d %d]", got, a, s.adminB.ID, ann.ID, bob.ID)
	}
	if strings.Contains(body, "@") {
		t.Fatalf("the response contains an email address: %s", body)
	}

	// Nothing hosted alone: empty lists, never null.
	code, out, body = soleHostEventsGET(t, s.a, ann.ID)
	if code != http.StatusOK || !strings.Contains(body, `"events":[]`) || !strings.Contains(body, `"candidates":[]`) {
		t.Fatalf("a person who hosts nothing: %d %s", code, body)
	}
	// A deleted target is 404; a non-admin cannot ask.
	if err := db.Exec("UPDATE users SET deleted_at = now() WHERE id = ?", dee.ID).Error; err != nil {
		t.Fatal(err)
	}
	if code, _, _ = soleHostEventsGET(t, s.a, dee.ID); code != http.StatusNotFound {
		t.Fatalf("deleted target: %d", code)
	}
	member := loginAPI(t, s.app, "ann", hostPW)
	if code, _, _ = soleHostEventsGET(t, member, tgt); code != http.StatusForbidden {
		t.Fatalf("non-admin: %d", code)
	}
}

// Discovery reads the same number of rows for one sole-hosted event as for
// sixty, and adds exactly its three reads when there are any. A deletion with
// one replacement for all sixty writes an add and a removal for each.
func TestSoleHostEventsQueryCountAndBulkDelete(t *testing.T) {
	reads := map[int]int64{}
	for _, n := range []int{0, 1, 60} {
		t.Run(fmt.Sprintf("%d events", n), func(t *testing.T) {
			s := newHostScene(t)
			var ids []uint
			for i := 0; i < n; i++ {
				ids = append(ids, soleHostEvent(t, s, fmt.Sprintf("Bulk %02d", i)).ID)
			}
			qc := countDBReads(t, s.app.DB)
			code, out, body := soleHostEventsGET(t, s.a, s.target.ID)
			reads[n] = qc.Reads()
			qc.Stop()
			if code != http.StatusOK || len(out.Events) != n {
				t.Fatalf("discovery of %d events: %d %s", n, code, body)
			}
			if n != 60 {
				return
			}
			r := extraUser(t, s, "replacement")
			picks := map[uint]uint{}
			for _, id := range ids {
				picks[id] = r.ID
			}
			if res := deleteAccount(s.a, s.target.ID, withHostPicks(picks)); res.code != http.StatusOK {
				t.Fatalf("delete with sixty replacements: %d %s", res.code, res.body)
			}
			if n := countRows(t, s.app.DB, "event_hosts", "user_id = ?", r.ID); n != 60 {
				t.Fatalf("the replacement hosts %d events, want 60", n)
			}
			if n := countRows(t, s.app.DB, "event_hosts", "user_id = ?", s.target.ID); n != 0 {
				t.Fatalf("the deleted account still has %d host rows", n)
			}
			added, addedMeta := auditRows(t, s.app, audit.EventHostAdded)
			removed, removedMeta := auditRows(t, s.app, audit.EventHostRemoved)
			if len(added) != 60 || len(removed) != 60 {
				t.Fatalf("audits: %d added, %d removed, want 60 each", len(added), len(removed))
			}
			for i, m := range addedMeta {
				var raw map[string]any
				if err := json.Unmarshal([]byte(added[i].MetaJSON), &raw); err != nil || len(raw) != 3 {
					t.Fatalf("host_added meta %s: %v", added[i].MetaJSON, err)
				}
				if m.UserID != r.ID || m.Reason != "account_deleted" || m.DepartingUserID != s.target.ID {
					t.Fatalf("host_added meta: %+v", m)
				}
			}
			for i, m := range removedMeta {
				var raw map[string]any
				if err := json.Unmarshal([]byte(removed[i].MetaJSON), &raw); err != nil || len(raw) != 2 {
					t.Fatalf("host_removed meta %s: %v", removed[i].MetaJSON, err)
				}
				if m.UserID != s.target.ID || m.Reason != "account_deleted" {
					t.Fatalf("host_removed meta: %+v", m)
				}
			}
		})
	}
	if reads[1] != reads[60] {
		t.Fatalf("reads grow with the events: %d for one, %d for sixty", reads[1], reads[60])
	}
	if reads[1] != reads[0]+3 {
		t.Fatalf("one event reads %d, none reads %d: want exactly three more (events, next dates, candidates)", reads[1], reads[0])
	}
}

// Every way a replacement plan can be wrong answers 400 needs_host_successor
// with the current events and candidates, and changes nothing.
func TestDeleteAccountNeedsEligibleHostSuccessors(t *testing.T) {
	s := newHostScene(t)
	db := s.app.DB
	a := s.adminA.ID
	one := soleHostEvent(t, s, "Sole one")
	two := soleHostEvent(t, s, "Sole two")
	ok := extraUser(t, s, "okhost")
	teen := extraUser(t, s, "teen")
	disabled := extraUser(t, s, "disabled")
	invited := extraUser(t, s, "invited")
	deleted := extraUser(t, s, "deleted")
	if err := db.Model(teen).Updates(map[string]any{"account_type": models.AccountTeen, "birth_date": time.Now().AddDate(-15, 0, 0)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(disabled).Update("status", models.UserDisabled).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(invited).Update("status", models.UserInvited).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE users SET deleted_at = now() WHERE id = ?", deleted.ID).Error; err != nil {
		t.Fatal(err)
	}
	g := activeGroup(t, s.app, s.adminA, "Group hosts", models.VisibilityPublic, "")
	groupEvent, _ := seedEvent(t, s.app, &g, a, feedStart(9))
	gone := soleHostEvent(t, s, "Soft deleted")
	if err := db.Exec("UPDATE events SET deleted_at = now() WHERE id = ?", gone.ID).Error; err != nil {
		t.Fatal(err)
	}
	notMine := churchEvent(t, s.a, "Not hosted by the target")

	good := map[uint]uint{one.ID: ok.ID, two.ID: ok.ID}
	with := func(extra map[uint]uint) map[uint]uint {
		m := map[uint]uint{}
		for k, v := range good {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	cases := []struct {
		name string
		body any
	}{
		{"no body", nil},
		{"no map", map[string]any{}},
		{"an entry is missing", withHostPicks(map[uint]uint{one.ID: ok.ID})},
		{"the target is the pick", withHostPicks(with(map[uint]uint{two.ID: s.target.ID}))},
		{"a teen", withHostPicks(with(map[uint]uint{two.ID: teen.ID}))},
		{"a disabled account", withHostPicks(with(map[uint]uint{two.ID: disabled.ID}))},
		{"an invited account", withHostPicks(with(map[uint]uint{two.ID: invited.ID}))},
		{"a deleted account", withHostPicks(with(map[uint]uint{two.ID: deleted.ID}))},
		{"an account that does not exist", withHostPicks(with(map[uint]uint{two.ID: 987654}))},
		{"a group event", withHostPicks(with(map[uint]uint{groupEvent.ID: ok.ID}))},
		{"a deleted event", withHostPicks(with(map[uint]uint{gone.ID: ok.ID}))},
		{"an unknown event", withHostPicks(with(map[uint]uint{987654: ok.ID}))},
		{"an event the target does not host", withHostPicks(with(map[uint]uint{notMine.ID: ok.ID}))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := accountState(t, db, s.target.ID)
			res := deleteAccount(s.a, s.target.ID, tc.body)
			if res.code != http.StatusBadRequest || !strings.Contains(res.body, `"code":"needs_host_successor"`) {
				t.Fatalf("%d %s", res.code, res.body)
			}
			out := decodeNeedHosts(t, res.body)
			if !sameIDs(eventIDs(out), one.ID, two.ID) {
				t.Fatalf("events = %v, want [%d %d]", eventIDs(out), one.ID, two.ID)
			}
			if got := candidateIDs(out); !sameIDs(got, a, s.adminB.ID, ok.ID) {
				t.Fatalf("candidates = %v", got)
			}
			if strings.Contains(res.body, "@") {
				t.Fatalf("the answer contains an email address: %s", res.body)
			}
			if after := accountState(t, db, s.target.ID); after != before {
				t.Fatalf("a refused deletion changed things:\n%s\n%s", before, after)
			}
		})
	}

	t.Run("malformed input is invalid", func(t *testing.T) {
		before := accountState(t, db, s.target.ID)
		tooMany := map[string]uint{}
		for i := 1; i <= maxHostSuccessors+1; i++ {
			tooMany[itoa(uint(i))] = ok.ID
		}
		path := "/api/v1/admin/users/" + itoa(s.target.ID)
		bodies := []struct {
			name string
			raw  string
		}{
			{"leading zero", `{"hostSuccessors":{"01":5}}`},
			{"zero key", `{"hostSuccessors":{"0":5}}`},
			{"negative key", `{"hostSuccessors":{"-1":5}}`},
			{"text key", `{"hostSuccessors":{"x":5}}`},
			{"zero value", `{"hostSuccessors":{"1":0}}`},
			{"wrong value type", `{"hostSuccessors":{"1":"five"}}`},
			{"wrong map type", `{"hostSuccessors":[1,2]}`},
			{"wrong successors type", `{"successors":"all"}`},
			{"malformed json", `{"hostSuccessors":`},
		}
		for _, b := range bodies {
			w := rawDelete(s.a, path, b.raw)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"invalid"`) {
				t.Fatalf("%s: %d %s", b.name, w.Code, w.Body.String())
			}
		}
		res := deleteAccount(s.a, s.target.ID, map[string]any{"hostSuccessors": tooMany})
		if res.code != http.StatusBadRequest || !strings.Contains(res.body, `"code":"invalid"`) {
			t.Fatalf("more than %d entries: %d %s", maxHostSuccessors, res.code, res.body)
		}
		if after := accountState(t, db, s.target.ID); after != before {
			t.Fatalf("a malformed request changed things:\n%s\n%s", before, after)
		}
	})
}

// A valid plan deletes the account, hands over the events, and records it.
func TestDeleteAccountHandsOverHosting(t *testing.T) {
	t.Run("one replacement", func(t *testing.T) {
		s := newHostScene(t)
		tc := loginAPI(t, s.app, "target", hostPW)
		ev := soleHostEvent(t, s, "Handover")
		tomb := tombstoneHost(t, s.app, ev.ID, s.adminA.ID, "ghost")
		r := extraUser(t, s, "replacement")
		res := deleteAccount(s.a, s.target.ID, withHostPicks(map[uint]uint{ev.ID: r.ID}))
		if res.code != http.StatusOK {
			t.Fatalf("delete: %d %s", res.code, res.body)
		}
		if got := hostRowUsers(t, s.app.DB, ev.ID); !sameIDs(got, r.ID) {
			t.Fatalf("host rows = %v, want only the replacement (the target and the old tombstone are gone)", got)
		}
		added, am := auditRows(t, s.app, audit.EventHostAdded)
		if len(added) != 1 || am[0].UserID != r.ID || am[0].Reason != "account_deleted" || am[0].DepartingUserID != s.target.ID ||
			added[0].ActorID == nil || *added[0].ActorID != s.adminA.ID {
			t.Fatalf("host_added: %+v %+v", added, am)
		}
		removed, rm := auditRows(t, s.app, audit.EventHostRemoved)
		if len(removed) != 2 {
			t.Fatalf("host_removed audits: %+v", removed)
		}
		reasons := map[uint]string{}
		for _, m := range rm {
			reasons[m.UserID] = m.Reason
		}
		if reasons[tomb.ID] != "deleted_host_cleanup" || reasons[s.target.ID] != "account_deleted" {
			t.Fatalf("host_removed reasons: %v", reasons)
		}
		apiDo(t, tc, "GET", "/api/v1/auth/me", nil, http.StatusUnauthorized)
		page, _ := getEvent(t, s.a, ev.ID, http.StatusOK)
		if len(page.Event.Hosts) != 1 {
			t.Fatalf("event page hosts: %+v", page.Event.Hosts)
		}
	})
	t.Run("an empty body deletes a person who hosts nothing alone", func(t *testing.T) {
		s := newHostScene(t)
		churchEvent(t, s.a, "Shared", s.target.Email) // {A, target}
		if res := deleteAccount(s.a, s.target.ID, nil); res.code != http.StatusOK {
			t.Fatalf("delete: %d %s", res.code, res.body)
		}
		if n := countRows(t, s.app.DB, "event_hosts", "user_id = ?", s.target.ID); n != 0 {
			t.Fatalf("%d host rows left", n)
		}
		if added, _ := auditRows(t, s.app, audit.EventHostAdded); len(added) != 0 {
			t.Fatalf("host_added audits: %d, want none", len(added))
		}
	})
	t.Run("a pick for an event the target co-hosts is honored", func(t *testing.T) {
		s := newHostScene(t)
		ev := churchEvent(t, s.a, "Shared", s.target.Email) // {A, target}
		other := churchEvent(t, s.a, "Shared two", s.target.Email)
		r := extraUser(t, s, "replacement")
		res := deleteAccount(s.a, s.target.ID, withHostPicks(map[uint]uint{ev.ID: r.ID, other.ID: s.adminA.ID}))
		if res.code != http.StatusOK {
			t.Fatalf("delete: %d %s", res.code, res.body)
		}
		if got := hostRowUsers(t, s.app.DB, ev.ID); !sameIDs(got, s.adminA.ID, r.ID) {
			t.Fatalf("host rows of the first: %v", got)
		}
		if got := hostRowUsers(t, s.app.DB, other.ID); !sameIDs(got, s.adminA.ID) {
			t.Fatalf("host rows of the second: %v", got)
		}
		// The pick who already hosted the second event is not added again, or
		// audited as added; the earlier adds of the target are not this deletion's.
		_, metas := auditRows(t, s.app, audit.EventHostAdded)
		var byDeletion int
		for _, m := range metas {
			if m.Reason == "account_deleted" {
				byDeletion++
				if m.UserID != r.ID {
					t.Fatalf("the deletion added %d", m.UserID)
				}
			}
		}
		if byDeletion != 1 {
			t.Fatalf("%d host_added audits from the deletion, want 1", byDeletion)
		}
	})
	t.Run("a disabled co-host keeps the event hosted", func(t *testing.T) {
		s := newHostScene(t)
		dee := extraUser(t, s, "dee")
		ev := churchEvent(t, s.a, "Disabled beside")
		onlyHosts(t, s.app.DB, ev.ID, s.adminA.ID, s.target.ID, dee.ID)
		apiDo(t, s.b, "PUT", "/api/v1/admin/users/"+itoa(dee.ID), map[string]any{"status": "disabled"}, http.StatusOK)
		if res := deleteAccount(s.a, s.target.ID, nil); res.code != http.StatusOK {
			t.Fatalf("delete: %d %s", res.code, res.body)
		}
		if got := hostRowUsers(t, s.app.DB, ev.ID); !sameIDs(got, dee.ID) {
			t.Fatalf("host rows: %v, want only the disabled co-host", got)
		}
	})
	t.Run("disabling a sole host needs no replacement and keeps the rows", func(t *testing.T) {
		s := newHostScene(t)
		ev := soleHostEvent(t, s, "Disabled sole")
		apiDo(t, s.a, "PUT", "/api/v1/admin/users/"+itoa(s.target.ID),
			map[string]any{"status": "disabled", "hostSuccessors": map[string]uint{itoa(ev.ID): 1}}, http.StatusOK)
		if got := hostRowUsers(t, s.app.DB, ev.ID); !sameIDs(got, s.target.ID) {
			t.Fatalf("host rows after disabling: %v", got)
		}
		r := extraUser(t, s, "replacement")
		path := fmt.Sprintf("/api/v1/events/%d/hosts", ev.ID)
		apiDo(t, s.a, "POST", path, map[string]any{"email": r.Email}, http.StatusOK)
		apiDo(t, s.a, "DELETE", fmt.Sprintf("%s/%d", path, s.target.ID), nil, http.StatusOK)
		if got := hostRowUsers(t, s.app.DB, ev.ID); !sameIDs(got, r.ID) {
			t.Fatalf("host rows after the admin's edits: %v", got)
		}
	})
	t.Run("group and event handoffs combine", func(t *testing.T) {
		s := newHostScene(t)
		ev := soleHostEvent(t, s, "Both")
		g := newTestGroup(t, s.app, s.target, "Led alone", models.GroupActive)
		r := extraUser(t, s, "replacement")
		groupPick := map[string]uint{itoa(g.ID): s.adminA.ID}
		hostPick := map[string]uint{itoa(ev.ID): r.ID}

		res := deleteAccount(s.a, s.target.ID, nil)
		if res.code != http.StatusBadRequest || !strings.Contains(res.body, `"code":"needs_host_successor"`) {
			t.Fatalf("both missing: %d %s", res.code, res.body)
		}
		res = deleteAccount(s.a, s.target.ID, map[string]any{"hostSuccessors": hostPick})
		if res.code != http.StatusBadRequest || !strings.Contains(res.body, `"code":"needs_successor"`) || !strings.Contains(res.body, `"groups"`) {
			t.Fatalf("only the group missing: %d %s", res.code, res.body)
		}
		if got := hostRowUsers(t, s.app.DB, ev.ID); !sameIDs(got, s.target.ID) {
			t.Fatalf("the refused deletion left host rows %v", got)
		}
		res = deleteAccount(s.a, s.target.ID, map[string]any{"hostSuccessors": hostPick, "successors": groupPick})
		if res.code != http.StatusOK {
			t.Fatalf("both given: %d %s", res.code, res.body)
		}
		if got := hostRowUsers(t, s.app.DB, ev.ID); !sameIDs(got, r.ID) {
			t.Fatalf("host rows: %v", got)
		}
		if n := countRows(t, s.app.DB, "memberships", "group_id = ? AND user_id = ? AND role = ? AND status = 'active'", g.ID, s.adminA.ID, models.MembershipLeader); n != 1 {
			t.Fatalf("the group's new leader is not in place")
		}
	})
	t.Run("a failure at the end rolls everything back", func(t *testing.T) {
		s := newHostScene(t)
		db := s.app.DB
		ev := soleHostEvent(t, s, "Rolled back")
		tomb := tombstoneHost(t, s.app, ev.ID, s.adminA.ID, "ghost")
		onlyHosts(t, db, ev.ID, s.adminA.ID, s.target.ID, tomb.ID)
		g := newTestGroup(t, s.app, s.target, "Rolled back group", models.GroupActive)
		r := extraUser(t, s, "replacement")
		key := nextHostKey()
		name := fmt.Sprintf("fold_test_fail_%d", key)
		if err := db.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN RAISE EXCEPTION 'injected failure'; END $$`, name)).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON audit_logs FOR EACH ROW
			WHEN (NEW.action = 'user.deleted') EXECUTE FUNCTION %s()`, name, name)).Error; err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			db.Exec("DROP TRIGGER IF EXISTS " + name + " ON audit_logs")
			db.Exec("DROP FUNCTION IF EXISTS " + name + "()")
		})
		before := accountState(t, db, s.target.ID)
		res := deleteAccount(s.a, s.target.ID, map[string]any{
			"hostSuccessors": map[string]uint{itoa(ev.ID): r.ID}, "successors": map[string]uint{itoa(g.ID): s.adminA.ID}})
		if res.code != http.StatusInternalServerError {
			t.Fatalf("injected failure: %d %s", res.code, res.body)
		}
		if after := accountState(t, db, s.target.ID); after != before {
			t.Fatalf("the failed deletion changed things:\n%s\n%s", before, after)
		}
		if got := hostRowUsers(t, db, ev.ID); !sameIDs(got, s.target.ID, tomb.ID) {
			t.Fatalf("host rows after the failure: %v", got)
		}
		if n := countRows(t, db, "memberships", "group_id = ? AND user_id = ? AND role = ? AND status = 'active'", g.ID, s.target.ID, models.MembershipLeader); n != 1 {
			t.Fatalf("the target is no longer the group's leader")
		}
	})
}

// A replacement who is disabled or deleted before the delete is refused; one
// who hosts after it cannot be deleted without a replacement of their own.
func TestDeleteAccountReplacementChanges(t *testing.T) {
	t.Run("disabled first", func(t *testing.T) {
		s := newHostScene(t)
		ev := soleHostEvent(t, s, "Replacement disabled")
		r := extraUser(t, s, "replacement")
		apiDo(t, s.b, "PUT", "/api/v1/admin/users/"+itoa(r.ID), map[string]any{"status": "disabled"}, http.StatusOK)
		res := deleteAccount(s.a, s.target.ID, withHostPicks(map[uint]uint{ev.ID: r.ID}))
		if res.code != http.StatusBadRequest || !strings.Contains(res.body, `"code":"needs_host_successor"`) {
			t.Fatalf("%d %s", res.code, res.body)
		}
		for _, id := range candidateIDs(decodeNeedHosts(t, res.body)) {
			if id == r.ID {
				t.Fatalf("a disabled account is offered as a host")
			}
		}
	})
	t.Run("deleted first", func(t *testing.T) {
		s := newHostScene(t)
		ev := soleHostEvent(t, s, "Replacement deleted")
		r := extraUser(t, s, "replacement")
		if res := deleteAccount(s.b, r.ID, nil); res.code != http.StatusOK {
			t.Fatalf("delete the replacement: %d %s", res.code, res.body)
		}
		res := deleteAccount(s.a, s.target.ID, withHostPicks(map[uint]uint{ev.ID: r.ID}))
		if res.code != http.StatusBadRequest || !strings.Contains(res.body, `"code":"needs_host_successor"`) {
			t.Fatalf("%d %s", res.code, res.body)
		}
		if got := hostRowUsers(t, s.app.DB, ev.ID); !sameIDs(got, s.target.ID) {
			t.Fatalf("host rows: %v", got)
		}
	})
	t.Run("the replacement is deleted after", func(t *testing.T) {
		s := newHostScene(t)
		ev := soleHostEvent(t, s, "Replacement then gone")
		r := extraUser(t, s, "replacement")
		r2 := extraUser(t, s, "second")
		if res := deleteAccount(s.a, s.target.ID, withHostPicks(map[uint]uint{ev.ID: r.ID})); res.code != http.StatusOK {
			t.Fatalf("delete the target: %d %s", res.code, res.body)
		}
		res := deleteAccount(s.a, r.ID, nil)
		if res.code != http.StatusBadRequest || !strings.Contains(res.body, `"code":"needs_host_successor"`) {
			t.Fatalf("delete the replacement: %d %s", res.code, res.body)
		}
		if got := eventIDs(decodeNeedHosts(t, res.body)); !sameIDs(got, ev.ID) {
			t.Fatalf("events = %v", got)
		}
		if res := deleteAccount(s.a, r.ID, withHostPicks(map[uint]uint{ev.ID: r2.ID})); res.code != http.StatusOK {
			t.Fatalf("delete with a second replacement: %d %s", res.code, res.body)
		}
	})
}

// A deletion is gated, as is removing a host, so the two cannot interleave:
// whichever comes second sees what the first left.
func TestDeleteAccountSerializesWithRemoveHost(t *testing.T) {
	setup := func(t *testing.T) (hostScene, eventJSON, *models.User) {
		s := newHostScene(t)
		c := extraUser(t, s, "cohost")
		ev := churchEvent(t, s.a, "Shared by two")
		onlyHosts(t, s.app.DB, ev.ID, s.adminA.ID, c.ID, s.target.ID)
		return s, ev, c
	}
	t.Run("remove first", func(t *testing.T) {
		s, ev, c := setup(t)
		db := s.app.DB
		key := nextHostKey()
		pauseOnStatement(t, db, "BEFORE DELETE", "event_hosts", key)
		release, hPID := holdAdvisory(t, db, key)
		remove := bgDo(s.a, "DELETE", fmt.Sprintf("/api/v1/events/%d/hosts/%d", ev.ID, c.ID), nil)
		removePID := waiterOn(t, db, hPID) // it holds the gate and the event
		del := bgDo(s.b, "DELETE", "/api/v1/admin/users/"+itoa(s.target.ID), nil)
		delPID := waiterOn(t, db, removePID)
		waitsOnGate(t, db, delPID)
		release()
		if r := awaitResult(t, remove); r.code != http.StatusOK {
			t.Fatalf("remove: %d %s", r.code, r.body)
		}
		r := awaitResult(t, del)
		if r.code != http.StatusBadRequest || !strings.Contains(r.body, `"code":"needs_host_successor"`) {
			t.Fatalf("delete after the co-host went: %d %s", r.code, r.body)
		}
		if got := eventIDs(decodeNeedHosts(t, r.body)); !sameIDs(got, ev.ID) {
			t.Fatalf("events = %v", got)
		}
	})
	t.Run("delete first", func(t *testing.T) {
		s, ev, c := setup(t)
		db := s.app.DB
		key := nextHostKey()
		pauseOnStatement(t, db, "BEFORE DELETE", "notifications", key)
		release, hPID := holdAdvisory(t, db, key)
		del := bgDo(s.b, "DELETE", "/api/v1/admin/users/"+itoa(s.target.ID), nil)
		delPID := waiterOn(t, db, hPID)
		remove := bgDo(s.a, "DELETE", fmt.Sprintf("/api/v1/events/%d/hosts/%d", ev.ID, c.ID), nil)
		removePID := waiterOn(t, db, delPID)
		waitsOnGate(t, db, removePID)
		release()
		if r := awaitResult(t, del); r.code != http.StatusOK {
			t.Fatalf("delete: %d %s", r.code, r.body)
		}
		r := awaitResult(t, remove)
		if r.code != http.StatusBadRequest || !strings.Contains(r.body, `"code":"needs_host"`) {
			t.Fatalf("remove the last host: %d %s", r.code, r.body)
		}
		if got := hostRowUsers(t, db, ev.ID); !sameIDs(got, c.ID) {
			t.Fatalf("host rows: %v", got)
		}
	})
}

// The deleting admin or the target changes while the delete waits for the gate.
func TestDeleteAccountRechecksUnderTheGate(t *testing.T) {
	changes := []struct {
		name string
		sql  string
	}{
		{"demoted", "UPDATE users SET role = 'member' WHERE id = ?"},
		{"disabled", "UPDATE users SET status = 'disabled' WHERE id = ?"},
		{"deleted", "UPDATE users SET deleted_at = now() WHERE id = ?"},
		{"session bumped", "UPDATE users SET session_version = session_version + 1 WHERE id = ?"},
	}
	for _, ch := range changes {
		t.Run("the admin is "+ch.name, func(t *testing.T) {
			s := newHostScene(t)
			ev := soleHostEvent(t, s, "Held")
			r := extraUser(t, s, "replacement")
			db := s.app.DB
			release, gPID := holdAdvisory(t, db, contactLockID)
			del := bgDo(s.a, "DELETE", "/api/v1/admin/users/"+itoa(s.target.ID), withHostPicks(map[uint]uint{ev.ID: r.ID}))
			waiterOn(t, db, gPID)
			if err := db.Exec(ch.sql, s.adminA.ID).Error; err != nil {
				t.Fatal(err)
			}
			before := accountState(t, db, s.target.ID)
			release()
			res := awaitResult(t, del)
			if res.code != http.StatusUnauthorized || !strings.Contains(res.body, `"code":"signed_out"`) {
				t.Fatalf("%d %s", res.code, res.body)
			}
			if after := accountState(t, db, s.target.ID); after != before {
				t.Fatalf("a refused deletion changed things:\n%s\n%s", before, after)
			}
			if got := hostRowUsers(t, db, ev.ID); !sameIDs(got, s.target.ID) {
				t.Fatalf("host rows: %v", got)
			}
		})
	}
	t.Run("the target is deleted by another admin", func(t *testing.T) {
		s := newHostScene(t)
		ev := soleHostEvent(t, s, "Held")
		r := extraUser(t, s, "replacement")
		db := s.app.DB
		release, gPID := holdAdvisory(t, db, contactLockID)
		del := bgDo(s.a, "DELETE", "/api/v1/admin/users/"+itoa(s.target.ID), withHostPicks(map[uint]uint{ev.ID: r.ID}))
		waiterOn(t, db, gPID)
		if err := db.Exec("UPDATE users SET deleted_at = now() WHERE id = ?", s.target.ID).Error; err != nil {
			t.Fatal(err)
		}
		release()
		res := awaitResult(t, del)
		if res.code != http.StatusNotFound || !strings.Contains(res.body, `"code":"not_found"`) {
			t.Fatalf("%d %s", res.code, res.body)
		}
		if n := countRows(t, db, "audit_logs", "action = ?", audit.EventHostAdded); n != 0 {
			t.Fatalf("%d host_added audits after a refused deletion", n)
		}
	})
}

// A church-wide event that only the target hosts has a room request. The real
// decision and the real deletion cannot deadlock in either order: the
// deletion locks the event (KEY SHARE) before the account, the decision locks
// the event (FOR UPDATE) before the host it notifies.
func TestRoomDecisionSerializesWithSoleHostErasure(t *testing.T) {
	for i, decision := range []string{"confirm", "reject"} {
		keyA, keyB := int64(7403300+2*i), int64(7403301+2*i)
		t.Run(decision, func(t *testing.T) {
			type scene struct {
				f      bookingFixture
				victim *models.User
				heir   *models.User
				ev     eventJSON
				decide func() error
				erase  func() error
			}
			setup := func(t *testing.T) scene {
				f, deleterUser := roomRequestsFixture(t)
				victim := newTestUser(t, f.app, "hostt", hostPW, models.RoleMember)
				heir := newTestUser(t, f.app, "heir", hostPW, models.RoleMember)
				var adm models.User
				if err := f.app.DB.Where("email = ?", testEmail("admin")).First(&adm).Error; err != nil {
					t.Fatal(err)
				}
				ev := churchEventAt(t, f.admin, "Sole hosted "+decision, f.startOn, victim.Email)
				apiDo(t, f.admin, "DELETE", fmt.Sprintf("/api/v1/events/%d/hosts/%d", ev.ID, adm.ID), nil, http.StatusOK)
				// Log in before the request creates its tasks: loginAPI drains notices.
				hostClient := loginAPI(t, f.app, "hostt", hostPW)
				hostClient.holdNotices = true
				deleter := loginAPI(t, f.app, "admin2", hostPW)
				deleter.holdNotices = true
				_ = deleterUser
				path := "/api/v1/events/" + itoa(ev.ID) + "/dates/" + feedDate(f.startOn) + "/room"
				if w := hostClient.do("PUT", path, map[string]any{"roomId": f.hall.ID}, true); w.Code != http.StatusOK {
					t.Fatalf("the host requests the room: %d %s", w.Code, w.Body.String())
				}
				reqs := listRequests(t, f.admin)
				if len(reqs) != 1 {
					t.Fatalf("requests: %+v", reqs)
				}
				return scene{f: f, victim: victim, heir: heir, ev: ev,
					decide: func() error {
						return doExpect(f.admin, "POST", "/api/v1/admin/room-requests/"+itoa(reqs[0].ID)+"/"+decision, map[string]any{}, http.StatusOK)
					},
					erase: func() error {
						return doExpect(deleter, "DELETE", "/api/v1/admin/users/"+itoa(victim.ID),
							withHostPicks(map[uint]uint{ev.ID: heir.ID}), http.StatusOK)
					},
				}
			}
			notices := func(t *testing.T, s scene, uid uint) int64 {
				return contactCount(t, s.f.app, &models.Notification{}, "user_id = ? AND kind = ?", uid, notify.RoomRequestDecided)
			}
			t.Run("decision first", func(t *testing.T) {
				s := setup(t)
				db := s.f.app.DB
				pauseOnStatement(t, db, "AFTER INSERT", "notifications", keyA)
				release, hPID := holdAdvisory(t, db, keyA)
				done := startOperation(s.decide)
				opPID := waiterOn(t, db, hPID) // it holds the event and the host's account, and has told the host
				erased := startOperation(s.erase)
				delPID := waiterOn(t, db, opPID)
				waitsOnRelation(t, db, delPID, "events")
				release()
				if err := awaitDBResult(t, done); err != nil {
					t.Fatalf("the decision: %v", err)
				}
				if err := awaitDBResult(t, erased); err != nil {
					t.Fatalf("the delete: %v", err)
				}
				if got := hostRowUsers(t, s.f.app.DB, s.ev.ID); !sameIDs(got, s.heir.ID) {
					t.Fatalf("host rows: %v", got)
				}
				if n := rowsFor(t, s.f.app.DB, "notifications", s.victim.ID); n != 0 {
					t.Fatalf("the erased host still has %d notices", n)
				}
			})
			t.Run("deletion first", func(t *testing.T) {
				s := setup(t)
				db := s.f.app.DB
				pauseOnStatement(t, db, "BEFORE DELETE", "notifications", keyB)
				release, hPID := holdAdvisory(t, db, keyB)
				erased := startOperation(s.erase)
				deleter := waiterOn(t, db, hPID) // it holds the gate, the event and the host's account
				done := startOperation(s.decide)
				opPID := waiterOn(t, db, deleter)
				waitsOnRelation(t, db, opPID, "events")
				release()
				if err := awaitDBResult(t, erased); err != nil {
					t.Fatalf("the delete: %v", err)
				}
				if err := awaitDBResult(t, done); err != nil {
					t.Fatalf("a decision that lost the race must finish: %v", err)
				}
				if n := notices(t, s, s.heir.ID); n != 1 {
					t.Fatalf("the replacement has %d decision notices, want 1", n)
				}
				if n := rowsFor(t, s.f.app.DB, "notifications", s.victim.ID); n != 0 {
					t.Fatalf("the erased host has %d notices", n)
				}
			})
		})
	}
}

// Adding the target to a co-hosted event commits before the delete starts:
// the deletion removes that row too.
func TestDeleteAccountRemovesRowsOfEventsItCoHosts(t *testing.T) {
	s := newHostScene(t)
	ev := churchEvent(t, s.a, "Co-hosted")
	apiDo(t, s.a, "POST", fmt.Sprintf("/api/v1/events/%d/hosts", ev.ID), map[string]any{"email": s.target.Email}, http.StatusOK)
	if res := deleteAccount(s.b, s.target.ID, nil); res.code != http.StatusOK {
		t.Fatalf("delete: %d %s", res.code, res.body)
	}
	if got := hostRowUsers(t, s.app.DB, ev.ID); !sameIDs(got, s.adminA.ID) {
		t.Fatalf("host rows: %v", got)
	}
}
