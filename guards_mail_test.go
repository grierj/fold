package main

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"fold/internal/guardkit"
)

// R6 (M7.5): notify.Send writes a durable delivery intent in the caller's
// transaction and the notify.deliver job it queues sends the email and the
// push. No caller delivers a notice itself after commit.
var legacyNoticeDeliveryNames = map[string]bool{
	"deliverNotificationEmails": true,
	"deliverNotificationEmail":  true,
	"recordDelivery":            true,
	"queuePushDeliveries":       true,
}

// mailerSendFiles may deliver application mail, each with its reason. The
// synchronous account exceptions below are methods, never whole-file grants.
var mailerSendFiles = map[string]string{
	"mail_send.go":           "sendTracked, the one place a sender is called; its callers are the files below",
	"notify_deliver.go":      "the notify.deliver job sends notice mail",
	"mail_jobs.go":           "direct account mail is sent by durable jobs after preparation commits",
	"messages_send.go":       "announcement mail goes through its own send path",
	"api_message_preview.go": "the composer's test send is synchronous by design (brief section 4)",
}

var synchronousMailMethods = map[string]struct{ method, reason string }{
	"api_profile.go": {"sendEmailChangeNotice", "the old address and cancellation capability cannot be rebuilt after verification"},
	"api_guest.go":   {"sendGuestConfirmationEmail", "the browser receives the same manage capability; delayed rotation would invalidate it"},
}

func mailSendAllowed(file string, parents []ast.Node) bool {
	if _, ok := mailerSendFiles[file]; ok {
		return true
	}
	exception, ok := synchronousMailMethods[file]
	if !ok || !immediateMailCall(parents) {
		return false
	}
	for i := len(parents) - 1; i >= 0; i-- {
		if fn, ok := parents[i].(*ast.FuncDecl); ok {
			if fn.Name.Name != exception.method || fn.Recv == nil || len(fn.Recv.List) != 1 {
				return false
			}
			recv := fn.Recv.List[0].Type
			if ptr, ok := recv.(*ast.StarExpr); ok {
				recv = ptr.X
			}
			name, ok := recv.(*ast.Ident)
			return ok && name.Name == "App"
		}
	}
	return false
}

func immediateMailCall(parents []ast.Node) bool {
	// A synchronous exception authorizes an immediate call, not a method value
	// that could escape to another function or be invoked with go/defer later.
	i := len(parents) - 2
	for i >= 0 {
		if _, ok := parents[i].(*ast.ParenExpr); !ok {
			break
		}
		i--
	}
	if i < 0 {
		return false
	}
	call, ok := parents[i].(*ast.CallExpr)
	if !ok || call.Fun != parents[i+1] {
		return false
	}
	for i := len(parents) - 1; i >= 0; i-- {
		switch parents[i].(type) {
		case *ast.GoStmt, *ast.DeferStmt, *ast.FuncLit:
			// Also keep exception-method calls out of transaction closures.
			return false
		}
	}
	return true
}

type mailSource uint8

const (
	mailSenderSource mailSource = 1 << iota
	mailUnwrapSource
	mailFactorySource
)

// mailSources follows parsed bindings, mail sender types, constructors and
// result positions, and package variables across files in the same directory.
// Keep sources once seen so a later assignment cannot hide a possible send.
// Field and method names are conservative matches, not resolved Go types. This
// is not interprocedural analysis: arbitrary helper bodies, type aliases and
// mail hidden behind untyped interfaces or containers still require review.
func mailSources(trees ...*ast.File) func(ast.Expr) mailSource {
	mailNames := map[*ast.Ident]bool{}
	dotNames := map[*ast.Ident]bool{}
	globals := map[string]*ast.Ident{}
	fields := map[string]mailSource{}
	results := map[string][]mailSource{}
	isSenderType := func(name string) bool {
		switch name {
		case "Sender", "SMTPSender", "Postmark", "DevOutbox", "Suppressing", "NoSender":
			return true
		}
		return false
	}
	packageSource := func(name string) mailSource {
		if name == "Unwrap" {
			return mailUnwrapSource
		}
		if strings.HasPrefix(name, "New") || name == "WithSuppressions" {
			return mailFactorySource
		}
		return 0
	}
	for _, tree := range trees {
		imports := guardkit.ImportNames(tree, "fold/internal/mail")
		ast.Inspect(tree, func(node ast.Node) bool {
			if id, ok := node.(*ast.Ident); ok && id.Obj == nil {
				mailNames[id] = imports[id.Name]
				dotNames[id] = imports["."]
			}
			return true
		})
		for _, decl := range tree.Decls {
			if d, ok := decl.(*ast.GenDecl); ok && d.Tok == token.VAR {
				for _, spec := range d.Specs {
					for _, id := range spec.(*ast.ValueSpec).Names {
						globals[id.Name] = id
					}
				}
			}
		}
	}
	typedSource := func(expr ast.Expr) mailSource {
		for {
			switch x := expr.(type) {
			case *ast.StarExpr:
				expr = x.X
			case *ast.ParenExpr:
				expr = x.X
			case *ast.Ident:
				if dotNames[x] && isSenderType(x.Name) {
					return mailSenderSource
				}
				return 0
			case *ast.SelectorExpr:
				if pkg, ok := x.X.(*ast.Ident); ok && mailNames[pkg] && isSenderType(x.Sel.Name) {
					return mailSenderSource
				}
				return 0
			default:
				return 0
			}
		}
	}
	//lint:ignore SA1019 Parse-only guard uses parser-local bindings, not type identity; unresolved flows are documented on TestMailGoesThroughJobs.
	bindings := map[*ast.Object]mailSource{}
	for _, tree := range trees {
		ast.Inspect(tree, func(node ast.Node) bool {
			switch x := node.(type) {
			case *ast.Field:
				for _, id := range x.Names {
					if id.Obj != nil {
						bindings[id.Obj] |= typedSource(x.Type)
					}
				}
			case *ast.StructType:
				for _, field := range x.Fields.List {
					for _, id := range field.Names {
						fields[id.Name] |= typedSource(field.Type)
					}
				}
			case *ast.ValueSpec:
				for _, id := range x.Names {
					if id.Obj != nil {
						bindings[id.Obj] |= typedSource(x.Type)
					}
				}
			case *ast.FuncDecl:
				if x.Type.Results != nil {
					var signature []mailSource
					for _, field := range x.Type.Results.List {
						for range max(1, len(field.Names)) {
							signature = append(signature, typedSource(field.Type))
						}
					}
					old := results[x.Name.Name]
					for len(old) < len(signature) {
						old = append(old, 0)
					}
					for i, value := range signature {
						old[i] |= value
					}
					results[x.Name.Name] = old
				}
			}
			return true
		})
	}
	var source func(ast.Expr) mailSource
	callResults := func(expr ast.Expr) []mailSource {
		for {
			if paren, ok := expr.(*ast.ParenExpr); ok {
				expr = paren.X
			} else {
				break
			}
		}
		call, ok := expr.(*ast.CallExpr)
		if !ok {
			return nil
		}
		if source(call.Fun)&(mailUnwrapSource|mailFactorySource) != 0 {
			return []mailSource{mailSenderSource}
		}
		fn := call.Fun
		for {
			if paren, ok := fn.(*ast.ParenExpr); ok {
				fn = paren.X
			} else {
				break
			}
		}
		switch fn := fn.(type) {
		case *ast.Ident:
			if fn.Obj != nil {
				if _, ok := fn.Obj.Decl.(*ast.FuncDecl); !ok {
					return nil
				}
			}
			return results[fn.Name]
		case *ast.SelectorExpr:
			return results[fn.Sel.Name]
		}
		return nil
	}
	source = func(expr ast.Expr) mailSource {
		switch x := expr.(type) {
		case *ast.Ident:
			if x.Obj != nil {
				return bindings[x.Obj]
			}
			if id := globals[x.Name]; id != nil && id.Obj != nil {
				return bindings[id.Obj]
			}
			if dotNames[x] {
				return packageSource(x.Name)
			}
		case *ast.SelectorExpr:
			if x.Sel.Name == "Mailer" {
				return mailSenderSource
			}
			if pkg, ok := x.X.(*ast.Ident); ok && mailNames[pkg] {
				return packageSource(x.Sel.Name)
			}
			return fields[x.Sel.Name]
		case *ast.CallExpr:
			if values := callResults(x); len(values) > 0 {
				return values[0]
			}
		case *ast.CompositeLit:
			return typedSource(x.Type)
		case *ast.UnaryExpr:
			if x.Op == token.AND {
				return source(x.X)
			}
		case *ast.TypeAssertExpr:
			return source(x.X) | typedSource(x.Type)
		case *ast.ParenExpr:
			return source(x.X)
		}
		return 0
	}
	for changed := true; changed; {
		changed = false
		bind := func(lhs ast.Expr, value mailSource) {
			switch x := lhs.(type) {
			case *ast.Ident:
				id := x
				if id.Obj == nil {
					id = globals[x.Name]
				}
				if id != nil && id.Obj != nil {
					old := bindings[id.Obj]
					bindings[id.Obj] |= value
					changed = changed || old != bindings[id.Obj]
				}
			case *ast.SelectorExpr:
				old := fields[x.Sel.Name]
				fields[x.Sel.Name] |= value
				changed = changed || old != fields[x.Sel.Name]
			}
		}
		bindValues := func(lhs, rhs []ast.Expr) {
			if len(rhs) == 1 && len(lhs) > 1 {
				if values := callResults(rhs[0]); len(values) > 0 {
					for i, value := range values {
						if i < len(lhs) {
							bind(lhs[i], value)
						}
					}
					return
				}
			}
			for i, value := range rhs {
				if i < len(lhs) {
					bind(lhs[i], source(value))
				}
			}
		}
		for _, tree := range trees {
			ast.Inspect(tree, func(node ast.Node) bool {
				switch x := node.(type) {
				case *ast.AssignStmt:
					bindValues(x.Lhs, x.Rhs)
				case *ast.ValueSpec:
					var lhs []ast.Expr
					for _, id := range x.Names {
						lhs = append(lhs, id)
					}
					bindValues(lhs, x.Values)
				}
				return true
			})
		}
	}
	return source
}

// Legacy delivery and runInBackground identifiers fail in all production
// packages. Only internal/mail owns sender implementations. Parsing ignores
// comments and quoted examples.
func noticeDeliveryViolations(file, src string, checkMailer bool) []string {
	parsed := guardkit.Sources{file: src}.Parse(0)
	if len(parsed.Problems) > 0 {
		return parsed.Problems
	}
	tree := parsed.Files[0].AST
	return parsedMailViolations(file, parsed.Fset, tree, mailSources(tree), checkMailer)
}

func parsedMailViolations(file string, positions *token.FileSet, tree *ast.File, source func(ast.Expr) mailSource, checkMailer bool) []string {
	var bad []string
	guardkit.InspectStack(tree, func(node ast.Node, parents []ast.Node) bool {
		at := positions.Position(node.Pos()).String()
		if id, ok := node.(*ast.Ident); ok {
			if legacyNoticeDeliveryNames[id.Name] {
				bad = append(bad, at+" (write the notice with notify.Send; delivery is automatic (R6))")
			}
			if id.Name == "runInBackground" {
				bad = append(bad, at+" (queue durable work with a jobs.Type's Enqueue; untracked background mail is forbidden)")
			}
		}
		// sendTracked is the sender call in a trench coat: it may be named only
		// where a send itself may be.
		if id, ok := node.(*ast.Ident); ok && checkMailer && id.Name == "sendTracked" && !mailSendAllowed(file, parents) {
			bad = append(bad, at+" (mail a notice with notify.Send or enqueue an ID-based direct-mail job; sendTracked is for delivery files)")
		}
		if sel, ok := node.(*ast.SelectorExpr); ok && checkMailer {
			// A named exception must stay synchronous at its call site too,
			// even when the caller is otherwise allowed to deliver mail.
			for _, exception := range synchronousMailMethods {
				if sel.Sel.Name == exception.method && !immediateMailCall(parents) {
					bad = append(bad, at+" (call synchronous mail methods immediately after commit; never use go, defer, closures or escaping method values)")
				}
			}
			if !mailSendAllowed(file, parents) && (sel.Sel.Name == "SendEmail" || (sel.Sel.Name == "Send" && source(sel.X)&mailSenderSource != 0)) {
				bad = append(bad, at+" (mail a notice with notify.Send or enqueue an ID-based direct-mail job; synchronous sends need a named exception)")
			}
		}
		return true
	})
	return bad
}

// productionMailViolations scans every production Go directory under root,
// regardless of nesting (guardkit.ReadProduction owns the skip rules).
func productionMailViolations(root string, checkMailer bool) ([]string, error) {
	sources, err := guardkit.ReadProduction(root)
	if err != nil {
		return nil, err
	}
	parsed := sources.Parse(0)
	bad := append([]string(nil), parsed.Problems...)
	for _, pkg := range parsed.Packages() {
		trees := make([]*ast.File, len(pkg.Files))
		for i, f := range pkg.Files {
			trees[i] = f.AST
		}
		source := mailSources(trees...)
		for _, f := range pkg.Files {
			mailer := checkMailer && f.Dir() != "internal/mail"
			bad = append(bad, parsedMailViolations(f.Path, parsed.Fset, f.AST, source, mailer)...)
		}
	}
	sort.Strings(bad)
	return bad, nil
}

func TestNoticesAreDeliveredBySendNotByCallers(t *testing.T) {
	bad, err := productionMailViolations(".", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range bad {
		t.Error(v)
	}
}

// TestMailGoesThroughJobs recursively parses production Go directories,
// including models, storage and migrations. Only internal/mail is exempt from
// the send checks, because it owns the sender implementations; the legacy
// post-commit notice helpers and runInBackground are forbidden there too.
// Tests, testdata, frontend dependencies, vendored code and hidden directories
// are excluded. Root delivery-file allowances do not grant permission to
// same-named nested files.
//
// The guard follows mail import aliases and dot imports, mail.Unwrap, assigned
// senders and package variables across files in one directory. It recognizes
// mail.Sender, SMTPSender, Postmark, DevOutbox, Suppressing and NoSender in
// typed parameters, fields, variables, assertions and literals. Same-package
// helper signatures track each result position, including multiple results;
// mail.New* and mail.WithSuppressions return a sender in the first position,
// including through assigned constructor functions. Add new sender types to
// this list and the guard together. Naming sendTracked counts as a send: only
// the delivery files (mailerSendFiles) and the named synchronous methods may.
//
// Synchronous exceptions allow sends only as immediate calls in their named App
// methods. Calls to those methods must also be immediate, never inside
// closures, go or defer, or captured as method values, even in delivery files.
// Transaction closures may finish before the call; the guard checks syntax, not
// whether a transaction committed. api_dev.go may unwrap and read the outbox,
// never send.
//
// Limits: this is parse-only analysis, not a type checker or call graph. Field
// and method names match conservatively. Arbitrary helper bodies,
// cross-package helper results, helper function values other than known mail
// constructors and Unwrap, type aliases, embedded forwarding and senders hidden
// in untyped interfaces or containers need review. It uses parser-local
// bindings with a narrow SA1019 exemption; it does not resolve Go type
// identity.
func TestMailGoesThroughJobs(t *testing.T) {
	bad, err := productionMailViolations(".", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range bad {
		t.Error(v)
	}
	for f := range mailerSendFiles {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("mailerSendFiles names %s, which does not exist: %v", f, err)
		}
	}
}

// These are complete Go source files in real package directories, not fragments
// tested with a manually selected checkMailer flag. Exercise discovery and the
// same-package resolver together, including the sender-implementation exclusion.
func TestMailGuardScansProductionPackages(t *testing.T) {
	for _, checkMailer := range []bool{false, true} {
		bad, err := productionMailViolations("testdata/mailguard", checkMailer)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"migrations/migration.go"}
		if checkMailer {
			want = append(want, "models/model.go", "storage/notify_deliver.go", "migrations/migration.go", "internal/deep/package/deliver.go", "internal/deep/package/deliver.go")
		}
		var got []string
		for _, v := range bad {
			file, _, _ := strings.Cut(v, ":")
			got = append(got, file)
		}
		sort.Strings(want)
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("checkMailer=%v: got %v, want %v; violations: %v", checkMailer, got, want, bad)
		}
	}
}

func TestMailGuardSkipsFixturesButNotProductionPackages(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{
		"models/model.go", "storage/store.go", "migrations/migration.go", "internal/one/two/three/worker.go", "internal/mail/legacy.go", "internal/web/worker.go",
		"example_test.go", "testdata/example.go", "web/node_modules/example/example.go", "vendor/example/example.go", ".git/example.go",
	} {
		file := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("package example\nfunc runInBackground() {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bad, err := productionMailViolations(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 6 {
		t.Fatalf("all six production directories must reject the identifier, including internal/mail and internal/web: %v", bad)
	}
}

// The scan catches a handler that delivers a notice after commit, and a
// direct mailer call from a file with no reason to hold one.
func TestNoticeGuardCatchesPostCommitDelivery(t *testing.T) {
	for name := range legacyNoticeDeliveryNames {
		post := "package main\nfunc example() { ids, err := notifyThing(tx); a." + name + "(ids) }\n"
		if bad := noticeDeliveryViolations("api_example.go", post, false); len(bad) != 1 || !strings.Contains(bad[0], "delivery is automatic") {
			t.Fatalf("guard missed legacy %s: %v", name, bad)
		}
	}
	mailer := "package main\nfunc example() { err := a.Mailer.Send(ctx, msg) }\n"
	if len(noticeDeliveryViolations("api_example.go", mailer, true)) != 1 {
		t.Fatal("guard missed a mailer call from a file outside the allow-list")
	}
	if len(noticeDeliveryViolations("notify_deliver.go", mailer, true)) != 0 {
		t.Fatal("guard flagged an allow-listed file")
	}
}

// sendTracked sends mail, so the guard treats naming it as it treats a send.
func TestMailGuardCatchesSendTrackedOutsideDelivery(t *testing.T) {
	src := "package main\nfunc example() { sendTracked(ctx, sender, msg, nil) }\n"
	if bad := noticeDeliveryViolations("api_example.go", src, true); len(bad) != 1 {
		t.Fatalf("guard missed sendTracked in a handler file: %v", bad)
	}
	for file := range mailerSendFiles {
		if bad := noticeDeliveryViolations(file, src, true); len(bad) != 0 {
			t.Errorf("%s may call sendTracked: %v", file, bad)
		}
	}
}

func TestMailGuardCatchesBypasses(t *testing.T) {
	for _, tc := range []struct {
		name, imports, body string
	}{
		{"direct", "", "a.Mailer.Send(ctx, msg)"},
		{"multiline direct", "", "a.\nMailer.\nSend (ctx, msg)"},
		{"assigned mailer", "", "sender := a.Mailer; sender.Send(ctx, msg)"},
		{"tracked", "", "sender.SendEmail(ctx, msg)"},
		{"direct unwrap", `import "fold/internal/mail"`, "mail.Unwrap(a.Mailer).Send(ctx, msg)"},
		{"aliased import", `import delivery "fold/internal/mail"`, "delivery.Unwrap(a.Mailer).Send(ctx, msg)"},
		{"assigned unwrap", `import "fold/internal/mail"`, "sender := mail.Unwrap(a.Mailer); sender.Send(ctx, msg)"},
		{"aliased assigned unwrap", `import delivery "fold/internal/mail"`, "sender := delivery.Unwrap(a.Mailer); sender.Send(ctx, msg)"},
		{"sender alias", `import "fold/internal/mail"`, "sender := mail.Unwrap(a.Mailer); alias := sender; alias.Send(ctx, msg)"},
		{"var sender", `import "fold/internal/mail"`, "var sender = mail.Unwrap(a.Mailer); sender.Send(ctx, msg)"},
		{"reassigned sender", `import "fold/internal/mail"`, "var sender Sender; sender = mail.Unwrap(a.Mailer); sender.Send(ctx, msg)"},
		{"asserted sender", `import "fold/internal/mail"`, "sender, ok := mail.Unwrap(a.Mailer).(*mail.DevOutbox); sender.Send(ctx, msg)"},
		{"parenthesized", `import "fold/internal/mail"`, "sender := (mail.Unwrap(a.Mailer)); (sender).Send(ctx, msg)"},
		{"assigned unwrap function", `import delivery "fold/internal/mail"`, "unwrap := delivery.Unwrap; sender := unwrap(a.Mailer); sender.Send(ctx, msg)"},
		{"dot import", `import . "fold/internal/mail"`, "sender := Unwrap(a.Mailer); sender.Send(ctx, msg)"},
		{"method value", `import "fold/internal/mail"`, "sender := mail.Unwrap(a.Mailer); send := sender.Send; send(ctx, msg)"},
		{"background call", "", "a.runInBackground(func() {})"},
		{"background alias", "", "background := a.runInBackground; background(f)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "package main\n" + tc.imports + "\nfunc (a *App) example() { " + tc.body + " }\n"
			bad := noticeDeliveryViolations("api_example.go", src, true)
			if len(bad) != 1 || strings.Contains(bad[0], "does not parse") {
				t.Fatalf("guard must reject the send or background identifier: %v", bad)
			}
		})
	}
	background := "package main\nfunc (a *App) runInBackground(f func()) { go f() }\n"
	if bad := noticeDeliveryViolations("internal/example/example.go", background, false); len(bad) != 1 || !strings.Contains(bad[0], "untracked background mail") {
		t.Fatalf("guard must reject reintroducing the helper in any production package: %v", bad)
	}
}

func TestMailGuardFollowsConcreteSendersAndConstructors(t *testing.T) {
	for _, senderType := range []string{"Sender", "SMTPSender", "Postmark", "DevOutbox", "Suppressing", "NoSender"} {
		for _, ptr := range []string{"", "*"} {
			for _, declaration := range []string{
				"func example(sender " + ptr + "delivery." + senderType + ") { sender.Send(ctx, msg) }",
				"func example() { var sender " + ptr + "delivery." + senderType + "; sender.Send(ctx, msg) }",
				"type holder struct { sender " + ptr + "delivery." + senderType + " }; func example(h holder) { h.sender.Send(ctx, msg) }",
			} {
				src := "package main\nimport delivery \"fold/internal/mail\"\n" + declaration
				if bad := noticeDeliveryViolations("api_example.go", src, true); len(bad) != 1 || strings.Contains(bad[0], "does not parse") {
					t.Fatalf("guard missed typed sender in %s: %v", declaration, bad)
				}
			}
		}
	}
	for _, body := range []string{
		"s, _ := delivery.NewSMTP(url, from); s.Send(ctx, msg)",
		"s, err := delivery.NewPostmark(token, from, transactional, broadcast); s.Send(ctx, msg)",
		"delivery.NewDevOutbox(db, from).Send(ctx, msg)",
		"delivery.WithSuppressions(db, inner).Send(ctx, msg)",
		"s := delivery.WithSuppressions(db, inner); s.Send(ctx, msg)",
		"delivery.NoSender{}.Send(ctx, msg)",
		"(&delivery.Postmark{}).Send(ctx, msg)",
		"s := &delivery.SMTPSender{}; s.Send(ctx, msg)",
		"s := delivery.DevOutbox{}; s.Send(ctx, msg)",
		"s := delivery.Suppressing{}; send := s.Send; consume(send)",
		"build := delivery.NewSMTP; s, err := build(url, from); s.Send(ctx, msg)",
		"wrap := delivery.WithSuppressions; s := wrap(db, inner); s.Send(ctx, msg)",
		"var s = delivery.NewDevOutbox(db, from); s.Send(ctx, msg)",
		"var s, err = delivery.NewSMTP(url, from); s.Send(ctx, msg)",
		"s := unknown.(*delivery.Postmark); s.Send(ctx, msg)",
	} {
		src := "package main\nimport delivery \"fold/internal/mail\"\nfunc example() { " + body + " }\n"
		if bad := noticeDeliveryViolations("api_example.go", src, true); len(bad) != 1 || strings.Contains(bad[0], "does not parse") {
			t.Fatalf("guard missed concrete sender or constructor in %s: %v", body, bad)
		}
	}
	for _, body := range []string{
		"var s *Postmark; s.Send(ctx, msg)",
		"NoSender{}.Send(ctx, msg)",
		"s, _ := NewSMTP(url, from); s.Send(ctx, msg)",
		"WithSuppressions(db, inner).Send(ctx, msg)",
	} {
		src := "package main\nimport . \"fold/internal/mail\"\nfunc example() { " + body + " }\n"
		if bad := noticeDeliveryViolations("api_example.go", src, true); len(bad) != 1 || strings.Contains(bad[0], "does not parse") {
			t.Fatalf("guard missed dot-imported sender in %s: %v", body, bad)
		}
	}
}

func TestMailGuardFollowsMultipleResultPositions(t *testing.T) {
	for _, tc := range []struct{ declaration, body string }{
		{"func mailer() (delivery.Sender, error) { return nil, nil }", "s, err := mailer(); s.Send(ctx, msg)"},
		{"func (a *App) mailer() (delivery.Sender, error) { return nil, nil }", "s, err := a.mailer(); s.Send(ctx, msg)"},
		{"func mailer() (error, *delivery.Postmark) { return nil, nil }", "err, s := mailer(); s.Send(ctx, msg)"},
		{"func mailer() (int, delivery.Sender, error) { return 0, nil, nil }", "_, s, err := mailer(); s.Send(ctx, msg)"},
		{"func mailer() (first, second delivery.Sender, err error) { return }", "_, s, err := mailer(); s.Send(ctx, msg)"},
		{"func mailer() (error, delivery.Sender) { return nil, nil }", "var err, s = mailer(); s.Send(ctx, msg)"},
		{"func mailer() (error, delivery.Sender) { return nil, nil }", "var s any; var err error; err, s = mailer(); s.Send(ctx, msg)"},
		{"func mailer() (delivery.Sender, error) { return nil, nil }", "var s, err = (mailer)(); s.Send(ctx, msg)"},
	} {
		src := "package main\nimport delivery \"fold/internal/mail\"\n" + tc.declaration + "\nfunc example() { " + tc.body + " }\n"
		if bad := noticeDeliveryViolations("api_example.go", src, true); len(bad) != 1 || strings.Contains(bad[0], "does not parse") {
			t.Fatalf("guard missed sender result in %s with %s: %v", tc.declaration, tc.body, bad)
		}
	}
	// A multi-result helper's error and other positions are not mail senders.
	src := `package main
import "fold/internal/mail"
func mailer() (other, mail.Sender, error) { return other{}, nil, nil }
func example() {
	x, _, err := mailer()
	x.Send(ctx, msg)
	err.Send(ctx, msg)
	var y, _, e = mailer()
	y.Send(ctx, msg)
	e.Send(ctx, msg)
}
`
	if bad := noticeDeliveryViolations("api_example.go", src, true); len(bad) != 0 {
		t.Fatalf("guard treated a non-sender result as mail: %v", bad)
	}
}

func TestMailGuardSynchronousMethodsStayImmediate(t *testing.T) {
	for file, exception := range synchronousMailMethods {
		call := "a." + exception.method + "(args)"
		for _, body := range []string{
			"go " + call,
			"defer " + call,
			"go func() { " + call + " }()",
			"func() { " + call + " }()",
			"db.Transaction(func(tx *gorm.DB) error { return " + call + " })",
			"send := a." + exception.method + "; go send(args)",
			"consume(a." + exception.method + ")",
		} {
			src := "package main\nfunc (a *App) handler() { " + body + " }\n"
			// Whole-file delivery allowances must not permit these methods to
			// escape either. Their provider call needs a waiting HTTP handler.
			for _, callerFile := range []string{file, "api_example.go", "mail_jobs.go", "notify_deliver.go"} {
				if bad := noticeDeliveryViolations(callerFile, src, true); len(bad) != 1 || !strings.Contains(bad[0], "call synchronous mail methods immediately after commit") {
					t.Fatalf("%s allowed %s: %v", callerFile, body, bad)
				}
			}
		}
		for _, body := range []string{
			call,
			"if err := " + call + "; err != nil { log(err) }",
			"(a." + exception.method + ")(args)",
			"err := db.Transaction(func(tx *gorm.DB) error { return save(tx) }); if err != nil { return }; " + call,
		} {
			src := "package main\nfunc (a *App) handler() { " + body + " }\n"
			if bad := noticeDeliveryViolations(file, src, true); len(bad) != 0 {
				t.Fatalf("%s rejected immediate post-commit call %s: %v", file, body, bad)
			}
		}
	}
}

func TestMailGuardScopesSynchronousExceptions(t *testing.T) {
	for file, exception := range synchronousMailMethods {
		t.Run(file, func(t *testing.T) {
			if exception.reason == "" {
				t.Fatal("synchronous exceptions must name their reason")
			}
			for _, send := range []string{
				"a.Mailer.Send(ctx, msg)",
				"sender := delivery.Unwrap(a.Mailer); sender.Send(ctx, msg)",
				"sender.SendEmail(ctx, msg)",
			} {
				for _, fn := range []struct {
					decl string
					want int
				}{
					{"func (a *App) " + exception.method + "()", 0},
					{"func (app *App) " + exception.method + "()", 0},
					{"func (a *App) unrelated()", 1},
					{"func " + exception.method + "()", 1},
					{"func (a *Other) " + exception.method + "()", 1},
				} {
					src := "package main\nimport delivery \"fold/internal/mail\"\n" + fn.decl + " { " + send + " }\n"
					if bad := noticeDeliveryViolations(file, src, true); len(bad) != fn.want {
						t.Fatalf("%s with %s: %v, want %d violations", fn.decl, send, bad, fn.want)
					}
					if bad := noticeDeliveryViolations("api_example.go", src, true); len(bad) != 1 {
						t.Fatalf("method name granted permission in another file: %v", bad)
					}
				}
			}
			for _, body := range []string{
				"go func() { a.Mailer.Send(ctx, msg) }()",
				"go a.Mailer.Send(ctx, msg)",
				"defer a.Mailer.Send(ctx, msg)",
				"sender := delivery.Unwrap(a.Mailer); go sender.Send(ctx, msg)",
				"sender := delivery.Unwrap(a.Mailer); defer sender.Send(ctx, msg)",
				"send := a.Mailer.Send; go send(ctx, msg)",
				"send := a.Mailer.Send; defer send(ctx, msg)",
				"consume(a.Mailer.Send)",
			} {
				src := "package main\nimport delivery \"fold/internal/mail\"\nfunc (a *App) " + exception.method + "() { " + body + " }\n"
				if bad := noticeDeliveryViolations(file, src, true); len(bad) != 1 || strings.Contains(bad[0], "does not parse") {
					t.Fatalf("guard granted the synchronous exception to %s: %v", body, bad)
				}
			}
		})
	}
}

func TestMailGuardAllowsDeliveryAndReadOnlyOutbox(t *testing.T) {
	send := "package main\nimport delivery \"fold/internal/mail\"\nfunc example() { a.Mailer.Send(ctx, msg); sender := delivery.Unwrap(a.Mailer); sender.Send(ctx, msg); sender.SendEmail(ctx, msg) }\n"
	for file, reason := range mailerSendFiles {
		if reason == "" {
			t.Fatalf("%s needs its delivery reason", file)
		}
		if bad := noticeDeliveryViolations(file, send, true); len(bad) != 0 {
			t.Errorf("%s delivery rejected: %v", file, bad)
		}
	}
	read := `package main
import delivery "fold/internal/mail"
func (a *App) devOutbox() {
	outbox, ok := delivery.Unwrap(a.Mailer).(*delivery.DevOutbox)
	outbox.List(100)
	outbox.HTMLOf(1)
}
`
	if bad := noticeDeliveryViolations("api_dev.go", read, true); len(bad) != 0 {
		t.Fatalf("read-only unwrap rejected: %v", bad)
	}
	readAndSend := strings.Replace(read, "outbox.List(100)", "outbox.List(100); outbox.Send(ctx, msg)", 1)
	if bad := noticeDeliveryViolations("api_dev.go", readAndSend, true); len(bad) != 1 {
		t.Fatalf("dev outbox was granted permission to send: %v", bad)
	}
	// Comments, strings, unrelated sends and shadowed bindings are not mail.
	other := `package main
import "fold/internal/mail"
func example() {
	// a.Mailer.Send(ctx, msg); runInBackground
	text := "deliverNotificationEmails and sender.SendEmail(ctx, msg)"
	sender := mail.Unwrap(a.Mailer)
	{ sender := other; sender.Send(ctx, msg) }
	{ mail := other; sender := mail.Unwrap(other); sender.Send(ctx, msg) }
}
`
	if bad := noticeDeliveryViolations("api_example.go", other, true); len(bad) != 0 {
		t.Fatalf("guard confused examples or unrelated bindings with mail: %v", bad)
	}
	// Provider implementations still own their Send/SendEmail methods.
	if bad := noticeDeliveryViolations("internal/mail/sender.go", send, false); len(bad) != 0 {
		t.Fatalf("guard rejected sender implementation calls: %v", bad)
	}
}
