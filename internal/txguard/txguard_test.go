package txguard

import (
	"testing"

	"gorm.io/gorm"
)

func TestBeginNeedsPluginAndTransaction(t *testing.T) {
	bare := &gorm.DB{Config: &gorm.Config{}, Statement: &gorm.Statement{}}
	if _, err := Begin(bare); err != ErrNotInstalled {
		t.Fatalf("want ErrNotInstalled, got %v", err)
	}
	if Installed(nil) {
		t.Fatal("a nil handle has no plugin")
	}
	with := &gorm.DB{Config: &gorm.Config{Plugins: map[string]gorm.Plugin{Name: Plugin{}}}, Statement: &gorm.Statement{}}
	if _, err := Begin(with); err != ErrNoTransaction {
		t.Fatalf("want ErrNoTransaction outside a transaction, got %v", err)
	}
}

// run feeds a span what the callbacks would for statements that ran: the
// observer before execution, then confirmRaw's created for a plain SAVEPOINT.
func run(s *Span, sqls ...string) {
	for _, q := range sqls {
		s.observe(q)
		if name, ok := savepointName(q); ok {
			s.created(name)
		}
	}
}

// What a span concludes from the statements it sees. A rollback to a name made
// since Begin is fine and keeps the name; a rollback to anything else, or text
// it cannot read that reaches a rollback, breaks the span.
func TestSpanReadsSavepointStatements(t *testing.T) {
	cases := []struct {
		name   string
		sqls   []string
		broken bool
	}{
		{"nothing", nil, false},
		{"ordinary statements", []string{"SELECT 1", "UPDATE users SET name = 'x'", "SELECT released_at FROM t"}, false},
		{"rollback to an older savepoint", []string{"ROLLBACK TO SAVEPOINT before"}, true},
		{"rollback to an older savepoint without the word", []string{"rollback to before"}, true},
		{"rollback work to", []string{"ROLLBACK WORK TO SAVEPOINT before"}, true},
		{"rollback to a savepoint made inside", []string{"SAVEPOINT a", "ROLLBACK TO SAVEPOINT a"}, false},
		{"rollback to a savepoint made inside, twice", []string{"SAVEPOINT a", "ROLLBACK TO a", "ROLLBACK TO a"}, false},
		{"case does not matter for unquoted names", []string{"SAVEPOINT Sp1", "ROLLBACK TO SAVEPOINT sP1;"}, false},
		{"nested inside, outer one rolled back", []string{"SAVEPOINT a", "SAVEPOINT b", "ROLLBACK TO a", "ROLLBACK TO a"}, false},
		{"a name undone by an earlier rollback is gone", []string{"SAVEPOINT a", "SAVEPOINT b", "ROLLBACK TO a", "ROLLBACK TO b"}, true},
		{"release then rollback reaches an older one of that name", []string{"SAVEPOINT a", "RELEASE SAVEPOINT a", "ROLLBACK TO a"}, true},
		{"release of an inner name keeps an outer inner one", []string{"SAVEPOINT a", "SAVEPOINT a", "RELEASE a", "ROLLBACK TO a"}, false},
		{"release of an older savepoint changes nothing", []string{"RELEASE SAVEPOINT before", "SAVEPOINT a", "ROLLBACK TO a"}, false},
		{"release of an older savepoint forgets inner names", []string{"SAVEPOINT a", "RELEASE SAVEPOINT before", "ROLLBACK TO a"}, true},
		{"new inner savepoint after older release remains usable", []string{"SAVEPOINT a", "RELEASE before", "SAVEPOINT b", "ROLLBACK TO b"}, false},
		{"release with an embedded comment is refused", []string{"SAVEPOINT a", "RELEASE /* comment */ SAVEPOINT a"}, true},
		{"savepoint with an embedded comment is refused", []string{"SAVEPOINT /* comment */ a"}, true},
		{"savepoint after a leading comment is refused", []string{"/* comment */ SAVEPOINT a"}, true},
		{"whole transaction rollback", []string{"ROLLBACK"}, true},
		{"abort", []string{"ABORT"}, true},
		{"rollback and chain", []string{"ROLLBACK AND CHAIN"}, true},
		{"SQL commit", []string{"COMMIT"}, true},
		{"SQL commit and chain", []string{"COMMIT AND CHAIN"}, true},
		{"SQL end", []string{"END WORK"}, true},
		{"SQL prepare transaction", []string{"PREPARE TRANSACTION 'id'"}, true},
		{"commit after a comment", []string{"/* x */ COMMIT"}, true},
		{"commit as a second statement", []string{"SELECT 1; COMMIT"}, true},
		{"commit attached to savepoint", []string{"SAVEPOINT a;COMMIT"}, true},
		{"end attached to release", []string{"SAVEPOINT a", "RELEASE a;END"}, true},
		{"end after a comment", []string{"-- x\nEND"}, true},
		{"end as a second statement", []string{"SELECT 1; END"}, true},
		{"end before a comment", []string{"END/* x */"}, true},
		{"ordinary case expression", []string{"SELECT CASE WHEN true THEN 1 ELSE 2 END"}, false},
		{"ordinary case expression with terminator", []string{"SELECT CASE WHEN true THEN 1 ELSE 2 END;"}, false},
		{"quoted names must match as written", []string{`SAVEPOINT "A"`, `ROLLBACK TO "A"`}, false},
		{"quoted and unquoted do not match", []string{`SAVEPOINT "a"`, `ROLLBACK TO a`}, true},
		{"a comment first is refused", []string{"/* x */ ROLLBACK TO SAVEPOINT a"}, true},
		{"a second statement is refused", []string{"SELECT 1; ROLLBACK TO a"}, true},
		{"a malformed rollback is refused", []string{"ROLLBACK TO"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Span{}
			run(s, c.sqls...)
			if got := s.Broken(); got != c.broken {
				t.Fatalf("Broken() = %v after %q, want %v", got, c.sqls, c.broken)
			}
		})
	}
}

func TestBrokenStaysBrokenAndNilSpanIsSafe(t *testing.T) {
	var nilSpan *Span
	if nilSpan.Broken() {
		t.Fatal("a nil span is not broken")
	}
	nilSpan.End()
	s := &Span{}
	run(s, "ROLLBACK TO older", "SAVEPOINT a", "ROLLBACK TO a")
	if !s.Broken() {
		t.Fatal("a broken span recovered")
	}
}

// A SAVEPOINT that did not run (a failed statement or a dry run) is read before
// execution but never created, so its name is not trusted: a later rollback to
// that name reaches an older savepoint.
func TestSavepointThatDidNotRunIsNotTrusted(t *testing.T) {
	s := &Span{}
	s.observe("SAVEPOINT older") // read, never confirmed
	s.observe("ROLLBACK TO SAVEPOINT older")
	if !s.Broken() {
		t.Fatal("a rollback to a savepoint that was never created kept the span")
	}

	// A failed one beside a real inner one of another name changes nothing.
	s = &Span{}
	run(s, "SAVEPOINT real")
	s.observe("SAVEPOINT failed")
	s.observe("ROLLBACK TO real")
	if s.Broken() {
		t.Fatal("a failed savepoint broke a rollback to a real one")
	}

	// A failed one with the name of a real inner one does not add a second copy:
	// releasing the real one leaves nothing for the rollback.
	s = &Span{}
	run(s, "SAVEPOINT a")
	s.observe("SAVEPOINT a")
	run(s, "RELEASE a")
	s.observe("ROLLBACK TO a")
	if !s.Broken() {
		t.Fatal("a failed duplicate savepoint stayed on the stack")
	}
}

// A savepoint in a name form savepointName cannot read is never trusted.
func TestSavepointNameOnlyReadsPlainStatements(t *testing.T) {
	for sql, want := range map[string]string{
		"SAVEPOINT a":              "a",
		"savepoint Sp1;":           "sp1",
		` SAVEPOINT "A" `:          `"A"`,
		"SAVEPOINT /* c */ a":      "",
		"/* c */ SAVEPOINT a":      "",
		"SAVEPOINT a;COMMIT":       "",
		"SAVEPOINT a; SAVEPOINT b": "",
		"SELECT 1":                 "",
		"RELEASE SAVEPOINT a":      "",
		"SAVEPOINT":                "",
		"SAVEPOINT a b":            "",
	} {
		got, ok := savepointName(sql)
		if got != want || ok != (want != "") {
			t.Errorf("savepointName(%q) = %q, %v; want %q", sql, got, ok, want)
		}
	}
}
