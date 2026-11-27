package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"fold/internal/accounts"
	"fold/internal/db"
	"fold/internal/groups"
	"fold/internal/people"
	"fold/internal/write"
)

// Admin people pages: find a person, disable or enable their account, and
// choose a successor for any group they alone lead (DEC-36).

type personRow struct {
	UID, Name, Email, Status string
	Admin                    bool
}

func findPeople(ctx context.Context, q db.Querier, search string, limit int) ([]personRow, error) {
	query := `SELECT uid, name, email, status, is_admin FROM people WHERE status IN ('active', 'disabled')`
	var args []any
	if search != "" {
		query += ` AND (name LIKE ? ESCAPE '\' OR email LIKE ? ESCAPE '\')`
		like := "%" + groups.EscapeLike(search) + "%"
		args = append(args, like, like)
	}
	query += ` ORDER BY name COLLATE NOCASE LIMIT ?`
	args = append(args, limit)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []personRow
	for rows.Next() {
		var p personRow
		if err := rows.Scan(&p.UID, &p.Name, &p.Email, &p.Status, &p.Admin); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (a *App) getAdminPeople(w http.ResponseWriter, r *http.Request) {
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(search) > 100 {
		search = search[:100]
	}
	var list []personRow
	f, ok := a.adminPage(w, r, func(q *sql.Tx, f *pageFacts) error {
		var err error
		list, err = findPeople(r.Context(), q, search, 200)
		return err
	})
	if !ok {
		return
	}
	f.Data = struct {
		People []personRow
		Search Field
		Query  string
	}{list, Field{ID: "q", Name: "q", Label: "Find a person", Hint: "By name or email.", Type: "search", Value: search, MaxLength: 100}, search}
	a.render(w, r, http.StatusOK, "admin_people", "", f.page)
}

type successorChoice struct {
	Group   groups.Group
	Choices []Choice
	Error   string
}

func (a *App) getAdminPerson(w http.ResponseWriter, r *http.Request) {
	a.showAdminPerson(w, r, http.StatusOK, "", nil)
}

func (a *App) showAdminPerson(w http.ResponseWriter, r *http.Request, status int, msg string, missing map[string]bool) {
	var p people.Person
	var mine []groups.Listed
	var succession []successorChoice
	found := false
	f, ok := a.adminPage(w, r, func(q *sql.Tx, f *pageFacts) error {
		var id int64
		err := q.QueryRowContext(r.Context(), `SELECT id FROM people WHERE uid = ? AND status IN ('active', 'disabled')`, r.PathValue("uid")).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		if p, err = people.ByID(r.Context(), q, id); err != nil {
			return err
		}
		if mine, err = groups.Mine(r.Context(), q, p.ID); err != nil {
			return err
		}
		sole, err := accounts.SoleLed(r.Context(), q, p.ID)
		if err != nil {
			return err
		}
		for _, g := range sole {
			sc := successorChoice{Group: g}
			if missing[g.UID] {
				sc.Error = "Choose who leads " + g.Name + " next."
			}
			rows, err := q.QueryContext(r.Context(), `SELECT p.uid, p.name, p.is_admin, EXISTS (SELECT 1 FROM active_memberships am WHERE am.group_id = ? AND am.person_id = p.id)
				FROM people p WHERE p.status = 'active' AND p.id <> ?
				AND (p.is_admin = 1 OR EXISTS (SELECT 1 FROM active_memberships am WHERE am.group_id = ? AND am.person_id = p.id))
				ORDER BY p.name COLLATE NOCASE`, g.ID, p.ID, g.ID)
			if err != nil {
				return err
			}
			for rows.Next() {
				var uid, name string
				var admin, member bool
				rows.Scan(&uid, &name, &admin, &member)
				hint := "A member of the group"
				if !member {
					hint = "An administrator"
				}
				sc.Choices = append(sc.Choices, Choice{ID: "succ-" + g.UID + "-" + uid, Name: "successor-" + g.UID, Type: "radio", Value: uid, Label: name, Hint: hint})
			}
			rows.Close()
			succession = append(succession, sc)
		}
		return nil
	})
	if !ok {
		return
	}
	if !found {
		a.notFound(w, r)
		return
	}
	self := f.session.Person.ID == p.ID
	f.Data = struct {
		Person     people.Person
		Groups     []groups.Listed
		Succession []successorChoice
		Self       bool
		Message    string
		Status     string
	}{p, mine, succession, self, msg, map[string]string{
		"disabled": "Disabled. They've been signed out and can't sign in until you turn their account on again.",
		"enabled":  "Turned on again. Their groups are back as they were.",
	}[r.URL.Query().Get("done")]}
	a.render(w, r, status, "admin_person", "", f.page)
}

func (a *App) postDisable(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.adminPage(w, r, nil); !ok {
		return
	}
	uid := r.PathValue("uid")
	succ := map[string]string{}
	for k, v := range r.PostForm {
		if g, ok := strings.CutPrefix(k, "successor-"); ok && len(v) == 1 && v[0] != "" {
			succ[g] = v[0]
		}
	}
	err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), accounts.Disable{PersonUID: uid, Successors: succ})
	switch {
	case errors.Is(err, write.ErrNotAllowed):
		// Say what is missing: a successor for each group they alone lead,
		// or why it can't be done at all.
		missing := map[string]bool{}
		var msg string
		a.reader.Read(r.Context(), func(q *sql.Tx) error {
			var id int64
			var admin bool
			if err := q.QueryRowContext(r.Context(), `SELECT id, is_admin FROM people WHERE uid = ?`, uid).Scan(&id, &admin); err != nil {
				return nil
			}
			sole, _ := accounts.SoleLed(r.Context(), q, id)
			for _, g := range sole {
				if succ[g.UID] == "" {
					missing[g.UID] = true
				}
			}
			switch {
			case len(missing) > 0:
				msg = "Choose who leads each group they alone lead, then disable."
			case admin:
				msg = "They're the only active administrator, or it's your own account, so it can't be disabled. Make someone else an administrator first."
			default:
				msg = "That could not be done. The page shows how things stand now."
			}
			return nil
		})
		a.showAdminPerson(w, r, http.StatusUnprocessableEntity, msg, missing)
		return
	case errors.Is(err, write.ErrSignedOut):
		http.Redirect(w, r, "/signin", http.StatusSeeOther)
		return
	case err != nil:
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/people/"+uid+"?done=disabled", http.StatusSeeOther)
}

func (a *App) postEnable(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.adminPage(w, r, nil); !ok {
		return
	}
	uid := r.PathValue("uid")
	err := a.exec.Run(r.Context(), write.Session(accounts.Hash(a.sessionToken(r))), accounts.Enable{PersonUID: uid})
	done := "?done=enabled"
	switch {
	case errors.Is(err, write.ErrNotAllowed):
		done = "" // already on: the page shows how it stands
	case errors.Is(err, write.ErrSignedOut):
		http.Redirect(w, r, "/signin", http.StatusSeeOther)
		return
	case err != nil:
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/people/"+uid+done, http.StatusSeeOther)
}
