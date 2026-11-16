package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"fold/internal/audit"
	"fold/internal/jobs"
	"fold/internal/mail"
	"fold/internal/policy"
	"fold/internal/testdb"
	"fold/models"
	"fold/storage"

	"gorm.io/gorm"
)

// R4-6d-V: the residue proof. TestErasureLeavesOnlyTheNamedResidue seeds a
// sentinel for the erased person (U), another living person (L) and the church
// (C) in every kind of place a deletion touches, runs the real DELETE, the
// tombstone sweep and avatar.cleanup, scans every text, JSON and byte column of
// every table and every file of the blob store, and holds what it finds to
// erasureResidueRules (erasure_residue.go). The rules, not this file, say what
// may stay; a rule with no PLAN disposition accepts nothing.
//
// The mutation map shows that each stage matters: it puts a stage's pre-erasure
// sentinels back, or takes a retention away, and the proof must fail on exactly
// the listed places. It edits what the scan saw, not the production code, so
// it proves the check, not the stage; the stages themselves are proved by
// their own tests.

// residueToken is one seeded sentinel. Rule is the rule that accepts it after
// the deletion; "" means the deletion must remove it everywhere.
type residueToken struct {
	Value    string
	Owner    residueOwner
	Scenario string
	Tag      string
	Rule     string
}

// residueRegistry hands out sentinels and remembers what each one expects.
type residueRegistry struct {
	byValue map[string]residueToken
	nextIP  map[residueOwner]int
}

func newResidueRegistry() *residueRegistry {
	return &residueRegistry{byValue: map[string]residueToken{}, nextIP: map[residueOwner]int{}}
}

var residueIPPrefix = map[residueOwner]string{residueU: "198.51.100.", residueL: "203.0.113.", residueC: "100.64.7."}

var residueTokenPattern = regexp.MustCompile(`snt-[ulc]-[a-z0-9]+-[a-z0-9]+|\b(?:198\.51\.100|203\.0\.113|100\.64\.7)\.\d{1,3}\b`)

// residueSQLPattern is residueTokenPattern in PostgreSQL's regular expressions.
const residueSQLPattern = `snt-[ulc]-|(198\.51\.100|203\.0\.113|100\.64\.7)\.[0-9]`

// text is a sentinel for a text field. Tags are lower-case letters and digits.
func (r *residueRegistry) text(owner residueOwner, scenario, tag, rule string) string {
	v := strings.ToLower(fmt.Sprintf("snt-%s-%s-%s", owner, scenario, tag))
	return r.add(residueToken{Value: v, Owner: owner, Scenario: scenario, Tag: tag, Rule: rule})
}

// ip is a sentinel for a network address, from a documentation range of its owner.
func (r *residueRegistry) ip(owner residueOwner, scenario, tag, rule string) string {
	r.nextIP[owner]++
	v := fmt.Sprintf("%s%d", residueIPPrefix[owner], r.nextIP[owner])
	return r.add(residueToken{Value: v, Owner: owner, Scenario: scenario, Tag: tag, Rule: rule})
}

func (r *residueRegistry) add(tok residueToken) string {
	if _, dup := r.byValue[tok.Value]; dup {
		panic("duplicate residue sentinel " + tok.Value)
	}
	r.byValue[tok.Value] = tok
	return tok.Value
}

// residueHit is one sentinel found in one place.
type residueHit struct {
	Table, Column, JSONKey, RowID string
	Token                         string
	// Value is the whole column value, or the JSON key's whole value.
	Value string
	Row   map[string]any
}

func (h residueHit) loc() string {
	l := h.Table + "." + h.Column
	if h.JSONKey != "" {
		l += "." + h.JSONKey
	}
	return l
}

func (h residueHit) identity() string {
	return h.loc() + "|" + h.RowID + "|" + h.Token
}

func residueTokensIn(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range residueTokenPattern.FindAllString(strings.ToLower(s), -1) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// residueWalk finds the sentinels in one column value, in its strings and in
// its object keys at every depth: a name used as a key hides as well as one
// used as a value. key is the top-level JSON key the value sits under ("" for
// a scalar column); a sentinel in a top-level key is reported under that key.
func residueWalk(table, column, key, rowID string, whole any, v any, row map[string]any, out *[]residueHit) {
	emit := func(tok, val string) {
		*out = append(*out, residueHit{Table: table, Column: column, JSONKey: key, RowID: rowID, Token: tok, Value: val, Row: row})
	}
	switch x := v.(type) {
	case string:
		for _, tok := range residueTokensIn(x) {
			val := x
			if key != "" {
				b, _ := json.Marshal(whole)
				val = string(b)
			}
			emit(tok, val)
		}
	case map[string]any:
		for k, sub := range x {
			top := key
			var w any = whole
			if key == "" {
				top, w = k, sub
			}
			if toks := residueTokensIn(k); len(toks) > 0 {
				val := k
				if key != "" {
					b, _ := json.Marshal(whole)
					val = string(b)
				} else {
					b, _ := json.Marshal(map[string]any{k: sub})
					val = string(b)
				}
				for _, tok := range toks {
					*out = append(*out, residueHit{Table: table, Column: column, JSONKey: top, RowID: rowID, Token: tok, Value: val, Row: row})
				}
			}
			residueWalk(table, column, top, rowID, w, sub, row, out)
		}
	case []any:
		for _, sub := range x {
			top := key
			var w any = whole
			if key == "" {
				top, w = "[]", x
			}
			residueWalk(table, column, top, rowID, w, sub, row, out)
		}
	}
}

// residueScan reads every row of every table that mentions a sentinel, and
// every file of the blob store. Rows are identified by primary key, so a row
// compares with itself before and after.
func residueScan(t *testing.T, db *gorm.DB, uploads string) []residueHit {
	t.Helper()
	var tables []struct{ Name, PK string }
	if err := db.Raw(`SELECT c.relname AS name,
		coalesce((SELECT string_agg(a.attname, ',' ORDER BY k.ord) FROM pg_index i
		CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY k(attnum, ord)
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum
		WHERE i.indrelid = c.oid AND i.indisprimary), '') AS pk
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND c.relname <> 'goose_db_version' ORDER BY c.relname`).Scan(&tables).Error; err != nil {
		t.Fatal(err)
	}
	var out []residueHit
	for _, tb := range tables {
		var rows []string
		q := fmt.Sprintf(`SELECT to_jsonb(r)::text FROM %q r WHERE to_jsonb(r)::text ~* ?`, tb.Name)
		if err := db.Raw(q, residueSQLPattern).Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		for _, raw := range rows {
			var row map[string]any
			if err := json.Unmarshal([]byte(raw), &row); err != nil {
				t.Fatal(err)
			}
			rowID := raw
			if tb.PK != "" {
				var parts []string
				for _, c := range strings.Split(tb.PK, ",") {
					parts = append(parts, fmt.Sprint(row[c]))
				}
				rowID = strings.Join(parts, ",")
			}
			for col, v := range row {
				residueWalk(tb.Name, col, "", rowID, v, v, row, &out)
			}
		}
	}
	// Byte columns come back from to_jsonb as hex, which hides text.
	var bytea []struct{ Table, Column string }
	if err := db.Raw(`SELECT table_name AS "table", column_name AS "column" FROM information_schema.columns
		WHERE table_schema = 'public' AND data_type = 'bytea'`).Scan(&bytea).Error; err != nil {
		t.Fatal(err)
	}
	for _, b := range bytea {
		var vals []string
		q := fmt.Sprintf(`SELECT encode(%q, 'escape') FROM %q WHERE encode(%q, 'escape') ~* ?`, b.Column, b.Table, b.Column)
		if err := db.Raw(q, residueSQLPattern).Scan(&vals).Error; err != nil {
			t.Fatal(err)
		}
		for i, v := range vals {
			for _, tok := range residueTokensIn(v) {
				out = append(out, residueHit{Table: b.Table, Column: b.Column, RowID: fmt.Sprintf("bytea:%d", i), Token: tok, Value: v})
			}
		}
	}
	err := filepath.WalkDir(uploads, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(uploads, p)
		rel = filepath.ToSlash(rel)
		body, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		for _, tok := range residueTokensIn(string(body)) {
			out = append(out, residueHit{Table: "(blob)", Column: strings.SplitN(rel, "/", 2)[0], RowID: rel, Token: tok, Value: string(body), Row: map[string]any{"path": rel}})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// residueEnabled says whether every PLAN anchor of the rule still matches.
func residueEnabled(rule erasureResidueRule) bool {
	for _, a := range rule.Plan {
		doc, err := os.ReadFile(a.File)
		if err != nil || !regexp.MustCompile(a.Pattern).Match(doc) {
			return false
		}
	}
	return len(rule.Plan) > 0
}

// residueCheck holds a scan to the rules.
type residueCheck struct {
	tokens  map[string]residueToken
	rules   map[string]erasureResidueRule
	enabled func(id string) bool
}

// residueRules is erasureResidueRules with each model name resolved to the
// table it is stored in, the way GORM names it.
func residueRules(t *testing.T, db *gorm.DB) []erasureResidueRule {
	t.Helper()
	tables := map[string]string{}
	for _, model := range models.All() {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(model); err != nil {
			t.Fatalf("parse %T: %v", model, err)
		}
		tables[stmt.Schema.Name] = stmt.Schema.Table
	}
	out := slices.Clone(erasureResidueRules)
	for i, r := range out {
		if r.Table == "(blob)" || r.Table == "(outside Fold)" {
			continue
		}
		table, ok := tables[r.Table]
		if !ok {
			t.Fatalf("rule %s names %q, which is not a model", r.RuleID, r.Table)
		}
		out[i].Table = table
	}
	return out
}

func newResidueCheck(reg *residueRegistry, rules []erasureResidueRule) residueCheck {
	c := residueCheck{tokens: reg.byValue, rules: map[string]erasureResidueRule{}}
	on := map[string]bool{}
	for _, r := range rules {
		c.rules[r.RuleID] = r
		on[r.RuleID] = residueEnabled(r)
	}
	c.enabled = func(id string) bool { return on[id] }
	return c
}

// sortedRules is the rules in rule-id order, so subtests run in a fixed order.
func (c residueCheck) sortedRules() []erasureResidueRule {
	out := make([]erasureResidueRule, 0, len(c.rules))
	for _, r := range c.rules {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RuleID < out[j].RuleID })
	return out
}

func (c residueCheck) accepts(rule erasureResidueRule, h residueHit) bool {
	if rule.Table != h.Table || rule.Column != h.Column || rule.JSONKey != h.JSONKey {
		return false
	}
	for col, want := range rule.Where {
		if h.Row == nil || fmt.Sprint(h.Row[col]) != want {
			return false
		}
	}
	for col, any := range rule.WhereIn {
		if h.Row == nil || !slices.Contains(any, fmt.Sprint(h.Row[col])) {
			return false
		}
	}
	return true
}

// problems is every way post breaks the rules, as one sorted, de-duplicated
// list of "kind place" lines, so a mutation can name exactly what it breaks.
//
//	unregistered  a sentinel nothing seeded
//	unaccepted    U text where no rule is named (the deletion had to remove it)
//	undispositioned RULE  the rule that would excuse it has no PLAN disposition
//	misplaced RULE  the rule excuses it somewhere else
//	moved RULE  the rule excuses the place, but not this row: the sentinel was seeded in another
//	missing RULE  a value the rule says must stay is gone
//	changed RULE  a value the rule says must stay is different
//	unseeded      a sentinel the world meant to plant was never planted
func (c residueCheck) problems(pre, post []residueHit) []string {
	set := map[string]bool{}
	add := func(kind, place string) { set[strings.TrimSpace(kind+" "+place)] = true }
	preBy := map[string]residueHit{}
	preTokens := map[string]bool{}
	for _, h := range pre {
		preBy[h.identity()] = h
		preTokens[h.Token] = true
	}
	for _, tok := range c.tokens {
		if !preTokens[tok.Value] {
			add("unseeded", tok.Scenario+"/"+tok.Tag)
		}
	}
	postBy := map[string]residueHit{}
	for _, h := range post {
		postBy[h.identity()] = h
		seeded, atSeed := preBy[h.identity()]
		tok, known := c.tokens[h.Token]
		if !known {
			add("unregistered", h.loc())
			continue
		}
		if tok.Owner != residueU {
			continue
		}
		rule, named := c.rules[tok.Rule]
		switch {
		case tok.Rule == "":
			add("unaccepted", h.loc())
		case !named:
			add("unaccepted", h.loc())
		case !c.enabled(rule.RuleID):
			add("undispositioned", rule.RuleID+" "+h.loc())
		case rule.Owner != tok.Owner || !c.accepts(rule, h):
			add("misplaced", rule.RuleID+" "+h.loc())
		case !atSeed || !c.accepts(rule, seeded):
			// A rule names a place, not every row of it: a MAY excuses the row its
			// PLAN disposition reached, so the same text on another row is residue.
			add("moved", rule.RuleID+" "+h.loc())
		}
	}
	for _, h := range pre {
		tok := c.tokens[h.Token]
		rule, named := c.rules[tok.Rule]
		if !named || rule.Fate != residueMust || !c.enabled(rule.RuleID) {
			continue
		}
		got, ok := postBy[h.identity()]
		switch {
		case !ok:
			add("missing", rule.RuleID)
		case got.Value != h.Value:
			add("changed", rule.RuleID)
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// residueMutation changes what the scan saw, as a stage that did not run or a
// retention that was skipped would have, and says exactly what must then fail.
type residueMutation struct {
	Name    string
	Apply   func(c *residueCheck, pre, post []residueHit) []residueHit
	Want    []string
	Disable string
}

// dropRule removes every value a retention rule requires, as skipping the
// retention would.
func dropRule(rule string) func(c *residueCheck, pre, post []residueHit) []residueHit {
	return func(c *residueCheck, pre, post []residueHit) []residueHit {
		var out []residueHit
		for _, h := range post {
			if c.tokens[h.Token].Rule != rule {
				out = append(out, h)
			}
		}
		return out
	}
}

// alterRule changes every value a retention rule requires to stay identical.
func alterRule(rule string) func(c *residueCheck, pre, post []residueHit) []residueHit {
	return func(c *residueCheck, pre, post []residueHit) []residueHit {
		out := append([]residueHit(nil), post...)
		for i, h := range out {
			if c.tokens[h.Token].Rule == rule {
				out[i].Value = h.Value + " (edited)"
			}
		}
		return out
	}
}

// residueBlobDir reports whether the store holds a folder for the owner.
func residueBlobDir(uploads, kind string, id uint) bool {
	fi, err := os.Stat(filepath.Join(uploads, kind, itoa(id)))
	return err == nil && fi.IsDir()
}

func residuePut(t *testing.T, store storage.Store, key, body string) {
	t.Helper()
	if err := store.Put(context.Background(), key, strings.NewReader(body), int64(len(body)), "image/jpeg"); err != nil {
		t.Fatalf("put %s: %v", key, err)
	}
}

// residueWorld is the seeded church: an acting admin, one living person (L)
// whose records the subjects share, and one erased subject per scenario.
type residueWorld struct {
	t        *testing.T
	s        coreErasureScene
	reg      *residueRegistry
	uploads  string
	l        *models.User
	lTeen    *models.User
	subjects map[string]*models.User
	// reasons are the contact-request ids of each scenario's review: the one
	// the admin keeps and the one they remove.
	keep, remove map[string]uint
	msg          map[string]uint
	occID        uint
	// optout and suppressed are the addresses whose hash records the opt-out
	// and suppression stages check.
	address map[string]string
}

var residueScenarios = []string{"adult", "teen", "guardian", "guest", "converted", "tombstone"}

func newResidueWorld(t *testing.T) *residueWorld {
	t.Helper()
	w := &residueWorld{t: t, s: newCoreErasureScene(t), reg: newResidueRegistry(),
		subjects: map[string]*models.User{}, keep: map[string]uint{}, remove: map[string]uint{}, msg: map[string]uint{}, address: map[string]string{}}
	w.uploads = avatarRootDir(w.s.app)
	w.s.ac.holdNotices = true
	w.l = newTestUser(t, w.s.app, "residue-living", erasurePW, models.RoleMember)
	w.lTeen = newTestUser(t, w.s.app, "residue-living-teen", erasurePW, models.RoleMember)
	w.exec(`UPDATE users SET phone = ?, name = 'Living' WHERE id = ?`, w.reg.text(residueL, "all", "phone", "L-user"), w.l.ID)
	key := fmt.Sprintf("avatars/%d/%032d.jpg", w.l.ID, 1)
	w.exec(`UPDATE users SET avatar_key = ? WHERE id = ?`, key, w.l.ID)
	residuePut(t, w.s.app.Uploads, key, "living picture "+w.reg.text(residueL, "all", "avatar", "L-avatar"))
	erasureMustCreate(t, w.s.app.DB, &models.Notification{UserID: w.l.ID, Kind: "residue.test", Category: "groups", Title: w.reg.text(residueL, "all", "notice", "L-notice"), Link: "/"})
	// Church settings an admin typed (O-13) and a church announcement whose
	// picture lives under messages/ (T24, DEC-118).
	for _, s := range []struct{ key, tag, rule string }{{"reply_to_address", "replyto", "O-13-reply-to"}, {"church_address", "churchaddr", "O-13-church-address"}} {
		w.exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, s.key, w.reg.text(residueC, "all", s.tag, s.rule)+"@example.test")
	}
	return w
}

func (w *residueWorld) exec(q string, args ...any) {
	w.t.Helper()
	if err := w.s.app.DB.Exec(q, args...).Error; err != nil {
		w.t.Fatalf("%s: %v", q, err)
	}
}

func (w *residueWorld) create(row any) {
	w.t.Helper()
	erasureMustCreate(w.t, w.s.app.DB, row)
}

func (w *residueWorld) audit(actor *uint, action, target string, id uint, meta map[string]any, ip string) {
	w.t.Helper()
	body, err := json.Marshal(meta)
	if err != nil {
		w.t.Fatal(err)
	}
	var ipp *string
	if ip != "" {
		ipp = &ip
	}
	auditRawLog(w.t, w.s.app.DB, actor, action, target, id, string(body), ipp)
}

// subject makes the scenario's erased person with every scalar field holding a
// sentinel the deletion has to remove, and the rows every account has.
func (w *residueWorld) subject(scenario string) *models.User {
	legacy := scenario == "tombstone"
	w.t.Helper()
	db := w.s.app.DB
	r := w.reg
	u := newTestUser(w.t, w.s.app, "residue-"+scenario, erasurePW, models.RoleMember)
	w.subjects[scenario] = u
	var email string
	if legacy {
		// The old handler lost the address; only id-keyed rows can be reached.
		email = fmt.Sprintf("deleted-%d@deleted.invalid", u.ID)
	} else {
		email = r.text(residueU, scenario, "email", "") + "@example.test"
		w.address[scenario] = email
	}
	w.exec(`UPDATE users SET email = ?, name = ?, display_name = ?, phone = ?, timezone = 'America/Boise' WHERE id = ?`,
		email, r.text(residueU, scenario, "name", ""), r.text(residueU, scenario, "display", ""), r.text(residueU, scenario, "phone", ""), u.ID)
	u.Email = email
	// A picture, with its words inside the bytes.
	key := fmt.Sprintf("avatars/%d/%032d.jpg", u.ID, 7)
	w.exec(`UPDATE users SET avatar_key = ? WHERE id = ?`, key, u.ID)
	residuePut(w.t, w.s.app.Uploads, key, "picture "+r.text(residueU, scenario, "avatar", ""))
	// Credentials.
	seedErasureTokens(w.t, db, u.ID, "residue-"+scenario, r.text(residueU, scenario, "newaddr", "")+"@example.test")
	w.create(&models.CalendarToken{UserID: u.ID, TokenHash: r.text(residueU, scenario, "calhash", ""), Scope: models.CalendarFeedMine})
	if scenario != "guest" { // a guest has no devices
		w.create(&models.PushSubscription{UserID: u.ID, Endpoint: "https://push.example.test/" + r.text(residueU, scenario, "pushendpoint", ""), P256dh: "k", Auth: "a"})
	}
	// Throttle keys the account owns, and one on a past address it does not.
	seedThrottleRows(w.t, db, ownedThrottleKeys(u.ID, heldAddress{value: email, ok: !legacy}))
	seedThrottleRows(w.t, db, []string{"emailchange:addr:" + r.text(residueU, scenario, "pastthrottle", "O-21") + "@example.test:h"})
	// Consent evidence.
	w.create(&models.Consent{UserID: u.ID, Channel: models.ConsentEmail, Scope: "category:groups", Status: models.ConsentOptedOut, Source: models.ConsentSourceWebForm,
		EvidenceText: r.text(residueU, scenario, "consentevidence", ""), IP: r.ip(residueU, scenario, "consentip", ""), UserAgent: r.text(residueU, scenario, "consentagent", "")})
	// Notices addressed to the person are deleted with them.
	w.create(&models.Notification{UserID: u.ID, Kind: "residue.test", Category: "groups", Title: r.text(residueU, scenario, "noticetitle", ""), Body: r.text(residueU, scenario, "noticebody", ""), Link: "/"})
	// The activity log: one row per class that attributes an address, S5, and
	// a registered row nobody can attribute.
	w.audit(&u.ID, audit.UserEmailChanged, "user", u.ID,
		map[string]any{"oldEmail": r.text(residueU, scenario, "oldemail", "") + "@example.test", "newEmail": r.text(residueU, scenario, "newemail", "") + "@example.test"},
		r.ip(residueU, scenario, "ipactor", ""))
	w.audit(nil, audit.InvitationAccepted, "invitation", 900000+u.ID, map[string]any{"userId": u.ID}, r.ip(residueU, scenario, "ipmetauser", ""))
	w.audit(nil, audit.PasswordResetRequested, "user", u.ID, map[string]any{}, r.ip(residueU, scenario, "iptarget", ""))
	w.audit(&u.ID, "legacy.retired.action", "user", u.ID, map[string]any{}, r.ip(residueU, scenario, "ips5", ""))
	w.audit(nil, audit.UserCreated, "user", u.ID, map[string]any{}, r.ip(residueU, scenario, "ipunattributed", "RETAIN_UNATTRIBUTED"))
	// A living admin acted on the erased person: the row names the person as its
	// target, but the address is the admin's own and must stay (6.5.2).
	w.audit(&w.l.ID, audit.UserDisabled, "user", u.ID, map[string]any{}, r.ip(residueL, scenario, "iplivingactor", "L-audit-ip"))
	// A message the person wrote: church content (RS-11). Its picture is in the
	// message's folder. The person's own recipient row carries an address and an
	// error the deletion clears; a guardian's copy keeps the guardian's address.
	msg := models.Message{AuthorID: &u.ID, Status: models.MessageSent, Subject: r.text(residueC, scenario, "msgsubject", "C-message-subject"), BodyText: r.text(residueC, scenario, "msgbody", "C-message-text")}
	w.create(&msg)
	w.msg[scenario] = msg.ID
	w.create(&models.MessageRecipient{MessageID: msg.ID, UserID: u.ID, Channel: "email", Address: r.text(residueU, scenario, "recipientaddr", "") + "@example.test", Status: "failed", Error: "bounced " + r.text(residueU, scenario, "recipienterror", "")})
	img := fmt.Sprintf("messages/%d/%032d.png", msg.ID, 3)
	residuePut(w.t, w.s.app.Uploads, img, "picture "+r.text(residueC, scenario, "msgimage", "C-message-image"))
	// A draft the person never sent: its words are theirs, and the church keeps
	// them with the message for audit (O-8). The subject and body are the
	// person's own text, so each is a U sentinel bound to its rule.
	w.create(&models.Message{AuthorID: &u.ID, Status: models.MessageDraft, Subject: r.text(residueU, scenario, "draftsubject", "O-8-subject"),
		BodyText: r.text(residueU, scenario, "draftbody", "O-8-body-text"), BodyHTML: "<p>" + r.text(residueU, scenario, "drafthtml", "O-8-body-html") + "</p>"})
	// A notice another person received that names the erased person: it was
	// rendered once, with no variables kept, and stays with its reader (O-16).
	w.create(&models.Notification{UserID: w.l.ID, Kind: "residue.test", Category: "groups", Title: "Update about " + r.text(residueU, scenario, "othernoticetitle", "O-16-title"),
		Body: r.text(residueU, scenario, "othernoticebody", "O-16"), Link: "/"})
	return u
}

// reasons seeds the admin's review: one reason to keep, one to remove, and the
// ages note of an add request, all the erased person's.
func (w *residueWorld) reasons(scenario string, u *models.User) {
	w.t.Helper()
	db := w.s.app.DB
	r := w.reg
	keep := erasureMakeRequest(w.t, db, policy.RequestContact, policy.ContactWithdrawn, u.ID, w.l.ID, 1, 0, 0, 1, "")
	remove := erasureMakeRequest(w.t, db, policy.RequestContact, policy.ContactWithdrawn, u.ID, w.l.ID, 1, 0, 0, 2, "")
	w.exec(`UPDATE contact_requests SET reason = ? WHERE id = ?`, "keep "+r.text(residueU, scenario, "reasonkeep", "T07-KEEP"), keep.ID)
	w.exec(`UPDATE contact_requests SET reason = ? WHERE id = ?`, "remove "+r.text(residueU, scenario, "reasonremove", ""), remove.ID)
	w.keep[scenario], w.remove[scenario] = keep.ID, remove.ID
	eventID, occ, groupID := erasureEventFixture(w.t, w.s)
	erasureMakeRequest(w.t, db, policy.RequestOccurrenceAdd, policy.ContactDenied, w.s.admin.ID, u.ID, groupID, eventID, occ.ID, 3, r.text(residueU, scenario, "agesnote", ""))
}

// households seeds the subject's one seat (a person holds at most one). When
// shared, the living person holds the household too and it must stay as it
// was (O-9); when the subject is its only holder, its name and address are the
// subject's and must go.
func (w *residueWorld) households(scenario string, u *models.User, shared bool) {
	w.t.Helper()
	r := w.reg
	relation := models.HouseholdRelationAdult
	if scenario == "teen" {
		relation = models.HouseholdRelationTeen
	}
	if scenario == "guardian" {
		relation = models.HouseholdRelationGuardian
	}
	if !shared {
		h := models.Household{Name: r.text(residueU, scenario, "soleName", ""), Address: r.text(residueU, scenario, "soleAddress", ""), CreatedBy: u.ID}
		w.create(&h)
		w.create(&models.HouseholdAccount{HouseholdID: h.ID, UserID: u.ID, Relation: relation})
		return
	}
	h := models.Household{Name: r.text(residueU, scenario, "sharedName", "O-9-name"), Address: r.text(residueL, scenario, "sharedAddress", "O-9-address"), CreatedBy: u.ID}
	w.create(&h)
	w.create(&models.HouseholdAccount{HouseholdID: h.ID, UserID: u.ID, Relation: relation})
	w.create(&models.HouseholdPerson{HouseholdID: h.ID, FirstName: r.text(residueL, scenario, "sharedkid", "L-household-person"), LastName: "Family", Kind: models.HouseholdChild, CreatedBy: w.l.ID})
}

// records seeds the rows addressed to the person: an invitation they accepted
// at an old address, a request approved for them, and the same two kinds of
// row on a past address no id reaches.
func (w *residueWorld) records(scenario string, u *models.User) {
	w.t.Helper()
	db := w.s.app.DB
	r := w.reg
	accepted := erasureInvitation(w.t, db, w.s.admin.ID, r.text(residueU, scenario, "invitedaddr", "")+"@example.test", true, nil)
	erasureAcceptedAudit(w.t, db, accepted, fmt.Sprintf(`{"userId":%d}`, u.ID))
	erasureInvitation(w.t, db, w.s.admin.ID, r.text(residueU, scenario, "pastinvited", "O-17-invitation")+"@example.test", false, nil)
	req := erasureRequest(w.t, db, r.text(residueU, scenario, "requestaddr", "")+"@example.test", models.RequestSelfSignup, models.RequestApproved, &u.ID, nil, nil)
	w.exec(`UPDATE account_requests SET name = ?, phone = ?, message = ?, guardian_email = ? WHERE id = ?`,
		r.text(residueU, scenario, "requestname", ""), r.text(residueU, scenario, "requestphone", ""), r.text(residueU, scenario, "requestmessage", ""), r.text(residueU, scenario, "requestguardian", "")+"@example.test", req)
	w.audit(nil, audit.AccountRequested, "account_request", req, map[string]any{"source": "self_signup"}, r.ip(residueU, scenario, "iplinked", ""))
	// A guardian address on a request no id reaches (O-17).
	other := erasureRequest(w.t, db, "someone-else@example.test", models.RequestSelfSignup, models.RequestPending, nil, nil, nil)
	w.exec(`UPDATE account_requests SET guardian_email = ? WHERE id = ?`, r.text(residueU, scenario, "pastguardian", "O-17-guardian-address")+"@example.test", other)
	w.create(&models.GuestSignup{Name: "Guest", Email: r.text(residueU, scenario, "pastguest", "O-17-guest-signup") + "@example.test", OccurrenceID: w.occurrence(), TokenHash: "residue-guest-" + scenario, ExpiresAt: time.Now().Add(time.Hour)})
}

// seedAdult is the member who holds households, reasons and requests.
func (w *residueWorld) seedAdult() {
	u := w.subject("adult")
	w.create(&models.Suppression{Channel: "email", AddressHash: mail.AddressHash(u.Email), Reason: models.SuppressHardBounce, Provider: "test"})
	w.households("adult", u, true)
	w.registration("adult", u)
	w.reasons("adult", u)
	w.records("adult", u)
}

// erase runs the real deletion for every account-holding scenario, the sweep
// for the legacy tombstone, and avatar.cleanup for every folder.
func (w *residueWorld) erase() {
	w.t.Helper()
	for _, sc := range residueScenarios {
		u := w.subjects[sc]
		if u == nil || sc == "tombstone" {
			continue
		}
		path := "/api/v1/admin/users/" + itoa(u.ID)
		items := allPages(w.t, w.s.ac, u.ID)
		body := map[string]any{"reasonReview": reviewOf(items, func(it reasonItemJSON) string {
			if it.RequestID == itoa(w.remove[sc]) {
				return "remove"
			}
			return "keep"
		})}
		if len(items) == 0 {
			body = nil
		}
		if res := deleteAccount(w.s.ac, u.ID, body); res.code != http.StatusOK {
			w.t.Fatalf("delete %s (%s): %d %s", sc, path, res.code, res.body)
		}
	}
}

// drain runs the sweep and the avatar cleanup through the job runner until no
// job is due.
func (w *residueWorld) drain() {
	w.t.Helper()
	a := w.s.app
	if _, err := enqueueJob(a.DB, kindTombstoneSweep, tombstonePageJob{Day: "2026-11-14"}, time.Now(), "residue-sweep", jobs.DefaultMaxAttempts); err != nil {
		w.t.Fatal(err)
	}
	if err := a.DB.Exec(`UPDATE jobs SET run_at = now() + interval '1 day' WHERE kind NOT IN (?, ?, ?, ?)`, kindTombstoneSweep, kindTombstonePage, kindTombstone, kindAvatarCleanup).Error; err != nil {
		w.t.Fatal(err)
	}
	kinds := append(a.erasureKinds(), a.avatarKinds()...)
	kinds = append(kinds, a.dailyKinds()...)
	r := a.newRunner(kinds...)
	for i := 0; i < 200; i++ {
		ran, err := r.RunOnce(context.Background(), "residue")
		if err != nil {
			w.t.Fatal(err)
		}
		if !ran {
			return
		}
	}
	w.t.Fatal("the sweep and cleanup jobs never drained")
}

func TestErasureLeavesOnlyTheNamedResidue(t *testing.T) {
	w := newResidueWorld(t)
	w.seedAdult()
	w.seedTeen()
	w.seedGuardian()
	w.seedGuest()
	w.seedConverted()
	w.seedTombstone()
	db := w.s.app.DB
	suppressionBefore := dumpRows(t, db, "suppressions", "id", "true")
	pre := residueScan(t, db, w.uploads)
	w.erase()
	w.drain()
	post := residueScan(t, db, w.uploads)
	check := newResidueCheck(w.reg, residueRules(t, db))
	residueStages(t, w, check, pre, post, suppressionBefore)
}

// residueSummary is what stayed, one line per place and rule, for the log.
func residueSummary(c residueCheck, pre, post []residueHit) []string {
	set := map[string]bool{}
	for _, h := range post {
		tok := c.tokens[h.Token]
		set[fmt.Sprintf("%s %s %s stays at %s", tok.Owner, tok.Scenario, tok.Rule, h.loc())] = true
	}
	out := []string{fmt.Sprintf("%d sentinels seeded, %d still found", len(pre), len(post))}
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out[1:])
	return out
}

// occurrence is the one future date every scenario registers for.
func (w *residueWorld) occurrence() uint {
	w.t.Helper()
	if w.occID == 0 {
		_, occ, _ := erasureEventFixture(w.t, w.s)
		w.occID = occ.ID
	}
	return w.occID
}

// registration seeds the person's place at an event, with the words a deletion
// clears from it.
func (w *residueWorld) registration(scenario string, u *models.User) {
	w.t.Helper()
	r := w.reg
	w.create(&models.Registration{OccurrenceID: w.occurrence(), UserID: &u.ID, Status: models.RegGoing, AttendanceMode: models.AttendanceInPerson, Source: "web",
		ManageTokenHash: r.text(residueU, scenario, "managetoken", ""), ChildcareAgesNote: r.text(residueU, scenario, "childcarenote", ""), RemovalNote: r.text(residueU, scenario, "removalnote", "")})
}

// safeguarding seeds a confirmed guardian link with the admin's note and a
// teen's consent with its evidence, each mirrored in the activity log. Both stay
// (O-4): the note and the evidence are why the church can say who agreed.
func (w *residueWorld) safeguarding(scenario string, guardian, teen uint) {
	w.t.Helper()
	r := w.reg
	now := time.Now().UTC()
	w.create(&models.GuardianLink{GuardianUserID: guardian, TeenUserID: &teen, Status: models.GuardianConfirmed, ConfirmedBy: &w.s.admin.ID,
		CheckNote: r.text(residueU, scenario, "checknote", "O-4-check-note"), DecidedAt: &now})
	w.audit(&w.s.admin.ID, audit.GuardianLinkConfirmed, "guardian_link", 800000+teen, map[string]any{"teenUserId": teen, "checkNote": r.text(residueU, scenario, "checknotemirror", "O-4-mirror-check-note")}, "")
	w.create(&models.TeenConsent{TeenUserID: teen, Scope: models.TeenConsentPhotos, Status: models.ConsentGranted, GrantedBy: guardian, GranterRole: models.ConsentGuardian,
		EvidenceText: r.text(residueU, scenario, "evidence", "O-4-evidence")})
	w.audit(&guardian, audit.TeenConsentGranted, "user", teen, map[string]any{"scope": models.TeenConsentPhotos, "evidenceText": r.text(residueU, scenario, "evidencemirror", "O-4-mirror-evidence")}, "")
}

func (w *residueWorld) seedTeen() {
	u := w.subject("teen")
	born := time.Now().UTC().AddDate(-15, 0, 0).Truncate(24 * time.Hour)
	w.exec(`UPDATE users SET account_type = 'teen', birth_date = ? WHERE id = ?`, born, u.ID)
	w.households("teen", u, false)
	w.records("teen", u)
	w.safeguarding("teen", w.l.ID, u.ID)
	// The guardian's copy of a message about the teen is the guardian's (M23).
	w.create(&models.MessageRecipient{MessageID: w.msg["teen"], UserID: w.l.ID, Channel: "email", Address: w.reg.text(residueL, "teen", "guardiancopy", "M23") + "@example.test", Status: "sent", CopyOfUserID: &u.ID})
}

func (w *residueWorld) seedGuardian() {
	u := w.subject("guardian")
	w.households("guardian", u, true)
	w.reasons("guardian", u)
	w.safeguarding("guardian", u.ID, w.lTeen.ID)
	// A copy the guardian received of a living teen's message: their own
	// address, which the deletion clears.
	w.create(&models.MessageRecipient{MessageID: w.msg["guardian"], UserID: u.ID, Channel: "email", Address: w.reg.text(residueU, "guardian", "copyaddr", "") + "@example.test", Status: "sent", CopyOfUserID: &w.lTeen.ID})
}

func (w *residueWorld) seedGuest() {
	u := w.subject("guest")
	w.exec(`UPDATE users SET status = ? WHERE id = ?`, models.UserGuest, u.ID)
	w.registration("guest", u)
	r := w.reg
	confirmed := time.Now().UTC()
	w.create(&models.GuestSignup{Name: r.text(residueU, "guest", "signupname", ""), Email: u.Email, OccurrenceID: w.occurrence(), TokenHash: "residue-confirmed-guest", ExpiresAt: time.Now().Add(time.Hour),
		ConfirmedAt: &confirmed, IP: r.ip(residueU, "guest", "signupip", ""), Timezone: r.text(residueU, "guest", "signupzone", "")})
	w.create(&models.GuestSignup{Name: r.text(residueU, "guest", "pendingname", ""), Email: u.Email, OccurrenceID: w.occurrence(), TokenHash: "residue-pending-guest", ExpiresAt: time.Now().Add(time.Hour)})
	w.records("guest", u)
}

// seedConverted is a person who was once a household person: the person row is
// the same individual (PERS) and goes with them. The conversion's activity-log
// rows show where the address of a caller nobody can name may stay.
func (w *residueWorld) seedConverted() {
	u := w.subject("converted")
	r := w.reg
	h := models.Household{Name: r.text(residueU, "converted", "soleName", ""), Address: r.text(residueU, "converted", "soleAddress", ""), CreatedBy: u.ID}
	w.create(&h)
	w.create(&models.HouseholdAccount{HouseholdID: h.ID, UserID: u.ID, Relation: models.HouseholdRelationAdult})
	born := time.Date(1990, 3, 4, 0, 0, 0, 0, time.UTC)
	person := models.HouseholdPerson{HouseholdID: h.ID, FirstName: r.text(residueU, "converted", "personfirst", ""), LastName: r.text(residueU, "converted", "personlast", ""), Kind: models.HouseholdAdult,
		BirthDate: &born, CareNotes: r.text(residueU, "converted", "carenotes", ""), CreatedBy: u.ID, ConvertedUserID: &u.ID}
	w.create(&person)
	w.registration("converted", u)
	// Completion and transfer rows no retained key ties to the erased person
	// (IR-1, IR-2, IR-4), and a nomination whose teen id is malformed under a
	// request the erased admin decided (IR-3).
	w.audit(&w.l.ID, audit.HouseholdPersonConverted, "household", h.ID, map[string]any{"personId": person.ID, "userId": u.ID, "invitationId": 1, "accountType": "adult", "transferCount": 1, "householdId": h.ID}, r.ip(residueU, "converted", "ir1", "IR-1"))
	w.audit(&w.l.ID, audit.RegistrationChanged, "registration", 1, map[string]any{"personId": person.ID, "userId": u.ID, "formerRegisteredBy": w.l.ID, "clearedChildcareRequested": false, "invitationId": 1}, r.ip(residueU, "converted", "ir2", "IR-2"))
	w.audit(&w.l.ID, audit.HouseholdPersonConverted, "other", 1, map[string]any{}, r.ip(residueU, "converted", "ir4", "IR-4"))
	inv := erasureInvitation(w.t, w.s.app.DB, w.s.admin.ID, "residue-convert@example.test", true, &person.ID)
	w.create(&models.AccountRequest{Name: "Converted", Email: "residue-convert@example.test", Source: models.RequestHouseholdConversion, Status: models.RequestApproved, ApprovedUserID: &u.ID, HouseholdPersonID: &person.ID,
		InvitationID: &inv, DecidedBy: &w.l.ID, ExpiresAt: time.Now().Add(time.Hour)})
	// A conversion the erased admin approved for another person.
	h2 := models.Household{Name: "Other family", CreatedBy: w.l.ID}
	w.create(&h2)
	person2 := models.HouseholdPerson{HouseholdID: h2.ID, FirstName: "Other", Kind: models.HouseholdTeen, CreatedBy: w.l.ID}
	w.create(&person2)
	inv2 := erasureInvitation(w.t, w.s.app.DB, w.l.ID, "residue-convert-two@example.test", true, &person2.ID)
	w.create(&models.AccountRequest{Name: "Other", Email: "residue-convert-two@example.test", Source: models.RequestHouseholdConversion, Status: models.RequestApproved, ApprovedUserID: &w.lTeen.ID, HouseholdPersonID: &person2.ID,
		InvitationID: &inv2, DecidedBy: &u.ID, ExpiresAt: time.Now().Add(time.Hour)})
	w.audit(&u.ID, audit.GuardianLinkRequested, "guardian_link", 1, map[string]any{"source": "household_conversion", "teenUserId": itoa(w.lTeen.ID)}, r.ip(residueU, "converted", "ir3", "IR-3"))
	// The same two nominations in their well-formed shapes: the erased admin as
	// approver, and as the member who invited the teen. Their addresses go.
	w.audit(&u.ID, audit.GuardianLinkRequested, "guardian_link", 2, map[string]any{"source": "household_conversion", "teenUserId": w.lTeen.ID}, r.ip(residueU, "converted", "ipapproval", ""))
	w.create(&models.AccountRequest{Name: "Invited", Email: "residue-invite@example.test", Source: models.RequestMemberInvite, Status: models.RequestApproved, ApprovedUserID: &w.lTeen.ID, RequestedBy: &u.ID, ExpiresAt: time.Now().Add(time.Hour)})
	w.audit(nil, audit.GuardianLinkRequested, "guardian_link", 3, map[string]any{"source": "signup", "teenUserId": w.lTeen.ID}, r.ip(residueU, "converted", "iprequester", ""))
}

// seedTombstone is an account the old handler deleted: it lost its address and
// left the rest, and only the sweep finishes the job.
func (w *residueWorld) seedTombstone() {
	u := w.subject("tombstone")
	r := w.reg
	h := models.Household{Name: r.text(residueU, "tombstone", "soleName", ""), Address: r.text(residueU, "tombstone", "soleAddress", ""), CreatedBy: u.ID}
	w.create(&h)
	w.create(&models.HouseholdAccount{HouseholdID: h.ID, UserID: u.ID, Relation: models.HouseholdRelationAdult})
	w.create(&models.HouseholdPerson{HouseholdID: h.ID, FirstName: r.text(residueU, "tombstone", "personfirst", ""), Kind: models.HouseholdAdult, CreatedBy: u.ID, ConvertedUserID: &u.ID})
	w.registration("tombstone", u)
	tombstoneLegacy(w.t, w.s.app.DB, u.ID)
}

// residueNoSentinel are the rules no scan can bind a sentinel to, and why.
var residueNoSentinel = map[string]string{
	"O-12":                    "outside Fold: no row or file of ours holds it",
	"RETAIN_ESSENTIAL":        "no action uses it (C3 table): the proof refuses it",
	"RETAIN-OPTOUT-HASH":      "hash only: checked by row in the opt-out stage",
	"RETAIN-SUPPRESSION-HASH": "hash only: checked by row in the suppression stage",
}

// residueMutations is the map from each stage to what the proof says when the
// stage did not run. skip puts the stage's pre-erasure sentinels back (a
// stage that clears a place and did not run leaves them there); Want is the
// exact set of problems it must produce, written by hand.
func residueMutations() []residueMutation {
	skip := func(name, scenario string, tags, locs, want []string) residueMutation {
		return residueMutation{Name: name, Want: want, Apply: func(c *residueCheck, pre, post []residueHit) []residueHit {
			out := append([]residueHit(nil), post...)
			for _, h := range pre {
				tok := c.tokens[h.Token]
				if tok.Owner != residueU || tok.Rule != "" {
					continue
				}
				if (len(tags) == 0 || slices.Contains(tags, tok.Tag)) && (len(locs) == 0 || slices.Contains(locs, h.loc())) && (scenario == "" || tok.Scenario == scenario) {
					out = append(out, h)
				}
			}
			return out
		}}
	}
	un := func(locs ...string) []string {
		var out []string
		for _, l := range locs {
			out = append(out, "unaccepted "+l)
		}
		return out
	}
	m := []residueMutation{
		skip("credentials", "", []string{"newaddr", "calhash", "pushendpoint"}, nil, un("account_tokens.payload.new_email", "calendar_tokens.token_hash", "push_subscriptions.endpoint")),
		skip("tombstone", "", []string{"name", "display", "phone", "email"}, []string{"users.name", "users.display_name", "users.phone", "users.email"}, un("users.name", "users.display_name", "users.phone", "users.email")),
		skip("household", "", []string{"soleName", "soleAddress"}, nil, un("households.name", "households.address")),
		skip("PERS", "", []string{"personfirst", "personlast", "carenotes"}, nil, un("household_people.first_name", "household_people.last_name", "household_people.care_notes")),
		skip("audit IP ACTOR", "", []string{"ipactor"}, nil, un("audit_logs.ip")),
		skip("audit IP META_USER", "", []string{"ipmetauser"}, nil, un("audit_logs.ip")),
		skip("audit IP TARGET_USER", "", []string{"iptarget"}, nil, un("audit_logs.ip")),
		skip("audit IP LINKED", "", []string{"iplinked"}, nil, un("audit_logs.ip")),
		skip("audit IP S5", "", []string{"ips5"}, nil, un("audit_logs.ip")),
		skip("audit IP C_APPROVAL", "", []string{"ipapproval"}, nil, un("audit_logs.ip")),
		skip("audit IP REQUESTER", "", []string{"iprequester"}, nil, un("audit_logs.ip")),
		skip("audit meta", "", []string{"oldemail", "newemail"}, nil, un("audit_logs.meta_json.oldEmail", "audit_logs.meta_json.newEmail")),
		skip("consent", "", []string{"consentevidence", "consentip", "consentagent"}, nil, un("consents.evidence_text", "consents.ip", "consents.user_agent")),
		skip("ages", "", []string{"agesnote"}, nil, un("contact_requests.add_childcare_ages_note")),
		skip("reason review", "", []string{"reasonremove"}, nil, un("contact_requests.reason")),
		skip("dependent invitations and requests", "", []string{"invitedaddr", "requestaddr", "requestname", "requestphone", "requestmessage", "requestguardian"},
			nil, un("invitations.email", "account_requests.email", "account_requests.name", "account_requests.phone", "account_requests.message", "account_requests.guardian_email")),
		skip("dependent guest signups", "", []string{"signupname", "signupip", "signupzone", "pendingname"}, nil, un("guest_signups.name", "guest_signups.ip", "guest_signups.timezone")),
		skip("dependent registrations", "", []string{"managetoken", "childcarenote", "removalnote"}, nil, un("registrations.manage_token_hash", "registrations.childcare_ages_note", "registrations.removal_note")),
		skip("dependent notices", "", []string{"noticetitle", "noticebody"}, nil, un("notifications.title", "notifications.body")),
		skip("messages", "", []string{"recipienterror", "recipientaddr", "copyaddr"}, nil, un("message_recipients.error", "message_recipients.address")),
		skip("throttle", "", []string{"email"}, []string{"login_throttles.key"}, un("login_throttles.key")),
		skip("avatar", "", []string{"avatar"}, nil, un("(blob).avatars")),
		skip("sweep", "tombstone", nil, nil, nil),
	}
	return m
}

// residueOptOutProblems and residueSuppressionProblems hold the two hash-only
// records to their own rules, apart from the scan: a retained plaintext address
// or a missing address-level opt-out would otherwise hide in any list of what
// may stay (RS-8). An erasure exports the held address's opted-out choices as
// hashes and never creates, clears or changes a suppression.
func residueOptOutProblems(optouts []models.EmailAddressOptOut, addr string) []string {
	var out []string
	found := false
	for _, o := range optouts {
		raw, _ := json.Marshal(o)
		if strings.Contains(strings.ToLower(string(raw)), strings.ToLower(addr)) {
			out = append(out, "plaintext address in the opt-outs")
		}
		if o.AddressHash == mail.AddressHash(addr) && o.Scope == "category:groups" {
			found = true
		}
	}
	if !found {
		out = append(out, "missing address-level opt-out")
	}
	return out
}

func residueSuppressionProblems(before, after, addr string) []string {
	var out []string
	if strings.Contains(strings.ToLower(after), strings.ToLower(addr)) {
		out = append(out, "plaintext address in the suppressions")
	}
	if !strings.Contains(after, mail.AddressHash(addr)) {
		out = append(out, "missing suppression hash")
	}
	if before != after {
		out = append(out, "the suppressions changed")
	}
	return out
}

func residueStages(t *testing.T, w *residueWorld, check residueCheck, pre, post []residueHit, suppBefore string) {
	db := w.s.app.DB
	t.Run("only the named residue stays", func(t *testing.T) {
		for _, p := range check.problems(pre, post) {
			t.Errorf("%s", p)
		}
		t.Log("\n" + strings.Join(residueSummary(check, pre, post), "\n"))
	})
	t.Run("each rule has a sentinel or says why not", func(t *testing.T) {
		used := map[string]bool{}
		for _, tok := range check.tokens {
			used[tok.Rule] = true
		}
		for _, r := range check.sortedRules() {
			if reason, none := residueNoSentinel[r.RuleID]; none {
				if used[r.RuleID] {
					t.Errorf("%s is listed as having no sentinel (%s) but one is seeded", r.RuleID, reason)
				}
				continue
			}
			if !used[r.RuleID] {
				t.Errorf("rule %s has no seeded sentinel", r.RuleID)
			}
			if !check.enabled(r.RuleID) {
				t.Errorf("rule %s is disabled but the world seeds a sentinel for it: its PLAN disposition is missing", r.RuleID)
			}
		}
	})
	t.Run("blobs", func(t *testing.T) {
		for _, sc := range residueScenarios {
			u := w.subjects[sc]
			if residueBlobDir(w.uploads, "avatars", u.ID) {
				t.Errorf("%s: avatars/%d/ is still in the store after avatar.cleanup", sc, u.ID)
			}
			if !residueBlobDir(w.uploads, "messages", w.msg[sc]) {
				t.Errorf("%s: messages/%d/ is church content and must stay", sc, w.msg[sc])
			}
		}
		if !residueBlobDir(w.uploads, "avatars", w.l.ID) {
			t.Errorf("another person's avatar folder was removed")
		}
	})
	t.Run("opt-out and suppression stay hash-only", func(t *testing.T) {
		var optouts []models.EmailAddressOptOut
		if err := db.Find(&optouts).Error; err != nil {
			t.Fatal(err)
		}
		suppAfter := dumpRows(t, db, "suppressions", "id", "true")
		for sc, addr := range w.address {
			if bad := residueOptOutProblems(optouts, addr); len(bad) != 0 {
				t.Errorf("%s: %v", sc, bad)
			}
		}
		addr := w.address["adult"]
		if bad := residueSuppressionProblems(suppBefore, suppAfter, addr); len(bad) != 0 {
			t.Errorf("adult: %v", bad)
		}
		// Each failure the stage names, shown by what would cause it.
		hash := mail.AddressHash(addr)
		for _, m := range []struct {
			name string
			got  []string
			want string
		}{
			{"opt-out never exported", residueOptOutProblems(nil, addr), "missing address-level opt-out"},
			{"plaintext in an opt-out", residueOptOutProblems(append(slices.Clone(optouts), models.EmailAddressOptOut{AddressHash: addr, Scope: "category:groups"}), addr), "plaintext address in the opt-outs"},
			{"suppression cleared", residueSuppressionProblems(suppBefore, "[]", addr), "missing suppression hash"},
			{"suppression changed", residueSuppressionProblems(suppBefore, strings.Replace(suppAfter, hash, hash+" ", 1), addr), "the suppressions changed"},
			{"plaintext in a suppression", residueSuppressionProblems(suppBefore, suppAfter+addr, addr), "plaintext address in the suppressions"},
		} {
			if !slices.Contains(m.got, m.want) {
				t.Errorf("%s: want %q among %v", m.name, m.want, m.got)
			}
		}
	})
	t.Run("mutation map", func(t *testing.T) {
		residueRunMutations(t, check, pre, post)
	})
}

func residueRunMutations(t *testing.T, check residueCheck, pre, post []residueHit) {
	if base := check.problems(pre, post); len(base) != 0 {
		t.Fatalf("the unmutated scan already fails: %v", base)
	}
	ran := 0
	run := func(m residueMutation) {
		t.Run(m.Name, func(t *testing.T) {
			c := check
			if m.Disable != "" {
				off := m.Disable
				inner := check.enabled
				c.enabled = func(id string) bool { return id != off && inner(id) }
			}
			mutated := m.Apply(&c, pre, post)
			if len(mutated) == len(post) && m.Disable == "" && !strings.HasPrefix(m.Name, "retention") {
				t.Fatalf("the mutation changed nothing the scan saw: it proves nothing")
			}
			got := c.problems(pre, mutated)
			want := slices.Clone(m.Want)
			sort.Strings(want)
			if !slices.Equal(got, want) {
				t.Errorf("fails with\n  %s\nwant exactly\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
			}
			ran++
		})
	}
	for _, m := range residueMutations() {
		if m.Name == "sweep" {
			// Without the sweep the legacy tombstone keeps every sentinel it
			// had: the wanted set is every place one was seeded for it.
			locs := map[string]bool{}
			for _, h := range pre {
				if tok := check.tokens[h.Token]; tok.Scenario == "tombstone" && tok.Owner == residueU && tok.Rule == "" {
					locs["unaccepted "+h.loc()] = true
				}
			}
			for l := range locs {
				m.Want = append(m.Want, l)
			}
		}
		run(m)
	}
	// Skipping a retention fails the rule that requires it, and only that one;
	// a rule with no PLAN disposition accepts nothing.
	for _, r := range check.sortedRules() {
		if !check.enabled(r.RuleID) {
			continue
		}
		bound := false
		for _, tok := range check.tokens {
			bound = bound || tok.Rule == r.RuleID
		}
		if !bound {
			continue
		}
		loc := r.Table + "." + r.Column
		if r.JSONKey != "" {
			loc += "." + r.JSONKey
		}
		if r.Fate == residueMust {
			run(residueMutation{Name: "retention skipped " + r.RuleID, Apply: dropRule(r.RuleID), Want: []string{"missing " + r.RuleID}})
			run(residueMutation{Name: "retention changed " + r.RuleID, Apply: alterRule(r.RuleID), Want: []string{"changed " + r.RuleID}})
		}
		if r.Owner == residueU && r.Fate == residueMay {
			// The same text, still in the place the rule names but on another row.
			id := r.RuleID
			run(residueMutation{Name: "retention moved " + id, Want: []string{"moved " + id + " " + loc},
				Apply: func(c *residueCheck, pre, post []residueHit) []residueHit {
					out := append([]residueHit(nil), post...)
					for i, h := range out {
						if c.tokens[h.Token].Rule == id {
							out[i].RowID = h.RowID + "/other"
						}
					}
					return out
				}})
		}
		if r.Owner == residueU {
			run(residueMutation{Name: "no disposition " + r.RuleID, Disable: r.RuleID, Apply: func(c *residueCheck, pre, post []residueHit) []residueHit { return post },
				Want: []string{"undispositioned " + r.RuleID + " " + loc}})
		}
	}
	if ran < 30 {
		t.Errorf("only %d mutations ran", ran)
	}
}

// The rules are only as good as the PLAN under them. This is the one with no
// authorized disposition (S1: remaining residue has none), and the proof
// refuses a sentinel it would excuse. A U sentinel in a place no enabled rule
// names, a message's text or a past address, is refused the same way.
func TestErasureResidueRefusesWhatThePlanDoesNotName(t *testing.T) {
	reg := newResidueRegistry()
	rules := residueRules(t, testdb.Shared(t))
	check := newResidueCheck(reg, rules)
	var off []string
	for _, r := range rules {
		if !check.enabled(r.RuleID) {
			off = append(off, r.RuleID)
		}
	}
	sort.Strings(off)
	if want := []string{"RETAIN_ESSENTIAL"}; !slices.Equal(off, want) {
		t.Fatalf("rules without a PLAN disposition = %v, want %v. A rule is enabled by a PLAN change, never by editing this list", off, want)
	}
	hit := func(table, column, key, tok string, row map[string]any) residueHit {
		return residueHit{Table: table, Column: column, JSONKey: key, RowID: "1", Token: tok, Value: tok, Row: row}
	}
	essential := reg.text(residueU, "x", "essential", "RETAIN_ESSENTIAL")
	messageText := reg.text(residueU, "x", "message", "")
	moved := reg.text(residueU, "x", "moved", "O-17-invitation")
	misowned := reg.text(residueU, "x", "misowned", "L-user")
	stray := reg.text(residueU, "x", "stray", "O-21")
	onOther := reg.text(residueU, "x", "onother", "O-17-invitation")
	lStray := reg.text(residueL, "x", "lstray", "L-user")
	pre := []residueHit{
		hit("audit_logs", "ip", "", essential, map[string]any{"action": "none"}),
		hit("messages", "body_text", "", messageText, nil), hit("invitations", "email", "", moved, nil), hit("users", "phone", "", misowned, nil),
		hit("login_throttles", "key", "", stray, nil), hit("users", "phone", "", lStray, nil),
		hit("invitations", "email", "", onOther, nil),
	}
	post := slices.Clone(pre)
	// The invitation address turned up in a column no rule names.
	post[2] = hit("invitations", "name", "", moved, nil)
	// An address a rule excuses, on another row of its column than the one seeded.
	post[len(pre)-1].RowID = "2"
	post = append(post, hit("somewhere", "else", "", "snt-u-nowhere-nothing", nil))
	got := check.problems(pre, post)
	want := []string{
		"misplaced O-17-invitation invitations.name",
		"misplaced L-user users.phone",
		"moved O-17-invitation invitations.email",
		"undispositioned RETAIN_ESSENTIAL audit_logs.ip",
		"unaccepted messages.body_text",
		"unregistered somewhere.else",
	}
	sort.Strings(want)
	// The sentinels the registry never planted are unseeded in pre as well.
	var filtered []string
	for _, p := range got {
		if !strings.HasPrefix(p, "unseeded") {
			filtered = append(filtered, p)
		}
	}
	if !slices.Equal(filtered, want) {
		t.Fatalf("got\n  %s\nwant\n  %s", strings.Join(filtered, "\n  "), strings.Join(want, "\n  "))
	}
}

// Every rule names a real place, and a place no table has is the typo this
// finds before a sentinel can be excused by it.
func TestErasureResidueRulesNameRealPlaces(t *testing.T) {
	db := testdb.Shared(t)
	cols := map[string]bool{}
	for _, f := range erasureCatalog(t, db) {
		cols[f.Table+"."+f.Column] = true
	}
	seen := map[string]bool{}
	for _, r := range residueRules(t, db) {
		if seen[r.RuleID] {
			t.Errorf("duplicate rule id %s", r.RuleID)
		}
		seen[r.RuleID] = true
		if r.Owner != residueU && r.Owner != residueL && r.Owner != residueC {
			t.Errorf("%s: owner %q", r.RuleID, r.Owner)
		}
		if r.Fate != residueMay && r.Fate != residueMust {
			t.Errorf("%s: fate %q", r.RuleID, r.Fate)
		}
		if len(r.Plan) == 0 || r.Why == "" {
			t.Errorf("%s: a rule names its PLAN disposition and says what stays", r.RuleID)
		}
		if r.Table == "(blob)" || r.Table == "(outside Fold)" {
			continue
		}
		if !cols[r.Table+"."+r.Column] {
			t.Errorf("%s: %s.%s is not a column", r.RuleID, r.Table, r.Column)
		}
		for col := range r.Where {
			if !cols[r.Table+"."+col] {
				t.Errorf("%s: Where names %s.%s, which is not a column", r.RuleID, r.Table, col)
			}
		}
		for col := range r.WhereIn {
			if !cols[r.Table+"."+col] {
				t.Errorf("%s: WhereIn names %s.%s, which is not a column", r.RuleID, r.Table, col)
			}
		}
	}
}

// A name used as a JSON key hides from a scan that reads only values. The walk
// reports a sentinel in a key at every depth, under its top-level key.
func TestResidueWalkReadsJSONKeys(t *testing.T) {
	reg := newResidueRegistry()
	top := reg.text(residueU, "x", "topkey", "")
	deep := reg.text(residueU, "x", "deepkey", "")
	inList := reg.text(residueU, "x", "listkey", "")
	val := reg.text(residueU, "x", "value", "")
	meta := map[string]any{
		top:    "plain",
		"nest": map[string]any{"a": map[string]any{deep: 1}},
		"list": []any{map[string]any{inList: true}},
		"v":    val,
	}
	var hits []residueHit
	residueWalk("audit_logs", "meta_json", "", "1", meta, meta, map[string]any{"action": "x"}, &hits)
	got := map[string]string{}
	for _, h := range hits {
		got[h.Token] = h.JSONKey
	}
	want := map[string]string{top: top, deep: "nest", inList: "list", val: "v"}
	if !maps.Equal(got, want) {
		t.Fatalf("walk found %v, want %v", got, want)
	}

	// No rule names a key as a place, so every key hit is unaccepted by
	// the rule a value would have had.
	check := newResidueCheck(reg, residueRules(t, testdb.Shared(t)))
	if probs := check.problems(hits, hits); !slices.Contains(probs, "unaccepted audit_logs.meta_json."+top) || !slices.Contains(probs, "unaccepted audit_logs.meta_json.nest") {
		t.Fatalf("a sentinel used as a key was excused: %v", probs)
	}
}

// The mirror rules excuse a note or evidence text only on the audit rows that
// carry one (15.5): a copy under any other action is a leak.
func TestResidueMirrorRulesNameTheirActions(t *testing.T) {
	reg := newResidueRegistry()
	rules := residueRules(t, testdb.Shared(t))
	check := newResidueCheck(reg, rules)
	note := reg.text(residueU, "x", "note", "O-4-mirror-check-note")
	evidence := reg.text(residueU, "x", "evidence", "O-4-mirror-evidence")
	hit := func(action, key, tok string) residueHit {
		return residueHit{Table: "audit_logs", Column: "meta_json", JSONKey: key, RowID: action, Token: tok, Value: tok, Row: map[string]any{"action": action}}
	}
	problems := func(h residueHit) []string {
		var out []string
		for _, p := range check.problems([]residueHit{h}, []residueHit{h}) {
			if !strings.HasPrefix(p, "unseeded") {
				out = append(out, p)
			}
		}
		return out
	}
	for _, ok := range []residueHit{
		hit(audit.GuardianLinkConfirmed, "checkNote", note), hit(audit.GuardianLinkRejected, "checkNote", note),
		hit(audit.TeenConsentGranted, "evidenceText", evidence), hit(audit.TeenConsentRevoked, "evidenceText", evidence),
	} {
		if p := problems(ok); len(p) != 0 {
			t.Errorf("%s on %s: %v", ok.JSONKey, ok.RowID, p)
		}
	}
	for _, bad := range []residueHit{
		hit("user.updated", "checkNote", note), hit(audit.GuardianLinkRequested, "checkNote", note), hit(audit.TeenConsentGranted, "checkNote", note),
		hit("user.updated", "evidenceText", evidence), hit(audit.GuardianLinkConfirmed, "evidenceText", evidence),
	} {
		want := "misplaced O-4-mirror-check-note audit_logs.meta_json.checkNote"
		if bad.Token == evidence {
			want = "misplaced O-4-mirror-evidence audit_logs.meta_json.evidenceText"
		}
		if p := problems(bad); !slices.Equal(p, []string{want}) {
			t.Errorf("%s copy on %s = %v, want %s", bad.JSONKey, bad.RowID, p, want)
		}
	}
}
