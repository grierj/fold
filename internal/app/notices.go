package app

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"fold/internal/accounts"
	"fold/internal/notices"
	"fold/internal/people"
	"fold/internal/policy"
	"fold/internal/write"
)

// The bell's list: opening a notice marks it read and goes where it points.

// noticeReader is a signed-in person as the notices package reads them.
func (a *App) noticeReader(p people.Person) notices.Reader {
	return notices.Reader{ID: p.ID, Admin: policy.IsAdmin(p.Policy()), Now: a.clock.Now()}
}

func (a *App) getNotices(w http.ResponseWriter, r *http.Request) {
	var list []notices.Notice
	f, ok := a.signedIn(w, r, func(q *sql.Tx, f *pageFacts) error {
		var err error
		list, err = notices.List(r.Context(), q, a.noticeReader(f.session.Person), 100)
		return err
	})
	if !ok {
		return
	}
	zone := viewerZone(f)
	items := make([]NoticeItem, 0, len(list))
	for _, n := range list {
		items = append(items, NoticeItem{ID: n.ID, Text: n.Text, Note: n.Note, When: stamp(n.At, zone), Read: n.Read, CSRF: a.csrfToken(r)})
	}
	f.Data = struct{ Notices []NoticeItem }{items}
	a.render(w, r, http.StatusOK, "notices", "", f.page)
}

// postNoticeOpen marks a notice read and goes where it points.
func (a *App) postNoticeOpen(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var n notices.Notice
	var gone bool
	_, ok := a.signedIn(w, r, func(q *sql.Tx, f *pageFacts) error {
		var err error
		n, err = notices.One(r.Context(), q, a.noticeReader(f.session.Person), id)
		if errors.Is(err, notices.ErrGone) {
			gone, err = true, nil
		}
		return err
	})
	if !ok {
		return
	}
	if gone {
		http.Redirect(w, r, "/notices", http.StatusSeeOther)
		return
	}
	if err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), notices.Open{ID: id}); err != nil && !errors.Is(err, write.ErrSignedOut) {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, n.Href, http.StatusSeeOther)
}

func (a *App) postNoticesRead(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.signedIn(w, r, nil); !ok {
		return
	}
	if err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), notices.ReadAll{}); err != nil && !errors.Is(err, write.ErrSignedOut) {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/notices", http.StatusSeeOther)
}
