package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"fold/internal/guardkit"
)

// ---------------------------------------------------------------------------
// R4-T07A: contact-request text changes stay in their helpers.
//
// Rules R-1 to R-7 (design.T07-A.guards) and the scoped reading of hard
// constraint H8 (RC-1 (b), (c), (d), (e)). Like the mail and suppression
// guards it parses production Go and reads syntax only.
// ---------------------------------------------------------------------------

// ctgLimits is the guard's stated reach. It is printed whenever the guard
// fails, and the self-test checks that it still names the SQL it cannot see.
const ctgLimits = "This guard is parse-only, like the mail and suppression guards. It is not a type checker or a call graph. " +
	"It binds names to models.ContactRequest only from declarations, parameters, results, composite literals, new, make, append, " +
	"type assertions, range values and := or = assignments, by the parser's lexical resolution within one file (a declaration that " +
	"shadows a name is its own binding, a closure sees the variable it captures, and a name declared in another file is not bound), " +
	"and it reads receiver chains syntactically plus a local variable of the same top-level declaration that is assigned exactly " +
	"once (counting assignments from closures, op-assignments and &v) from a chain. It reads constant SQL by quote: a semicolon in " +
	"a string, E-string, dollar quote or double-quoted identifier does not end a statement, and a comment or an unclosed quote makes " +
	"the rest of the literal one statement. Same-file string constants resolve by the same lexical rule as variables (a constant that " +
	"a closure or block redeclares is its own; a map-literal key constant must be declared before its use). The callback's *gorm.DB " +
	"argument to eraseContactRequestTextIn must resolve to eraseAccountIn's own parameter, and the one to eraseAccountIn to the erasure callback's own parameter. It does not see SQL built from non-literal pieces (computed-table-name SQL such as " +
	"internal/testdb's 'DELETE FROM ' + t is not seen, nor is fmt.Sprintf with non-constant arguments), constants from other files " +
	"or packages, table or column names held in variables, reflection, generics, type aliases, embedded structs, ContactRequest " +
	"values or *gorm.DB chains that arrive from another function's result, a parameter, a field or an interface, reassigned chain " +
	"variables, gorm.Expr or clause values built at run time, GORM scopes or callbacks that add clauses, Create with a map on a " +
	"contact_requests model, the order of assignments inside apiCreateAddRequest, a write through a pointer to a field (&r.Reason), " +
	"or SQL sent around GORM (database/sql or pgx directly; Fold sends none). Within those limits it rejects every form named by " +
	"R-1 to R-7 and RC-1 (b) to (e); beyond them only the database triggers and review apply."

var (
	ctgProtectedWord  = regexp.MustCompile(`(?i)(?:^|[^a-z0-9_])(?:reason|add_childcare_ages_note|addchildcareagesnote)(?:[^a-z0-9_]|$)`)
	ctgTableName      = regexp.MustCompile(`^(?:public\.)?contact_requests(?: (?:as )?[a-z_][a-z0-9_]*)?$`)
	ctgUpdateSQL      = regexp.MustCompile(`\bupdate (?:only )?(?:public\.)?contact_requests\b`)
	ctgInsertSQL      = regexp.MustCompile(`\binsert into (?:public\.)?contact_requests\b`)
	ctgMergeSQL       = regexp.MustCompile(`\bmerge into (?:only )?(?:public\.)?contact_requests\b`)
	ctgDeleteSQL      = regexp.MustCompile(`\bdelete from (?:only )?(?:public\.)?contact_requests\b`)
	ctgTruncateSQL    = regexp.MustCompile(`\btruncate\b[^;]*\bcontact_requests\b`)
	ctgSetKeyword     = regexp.MustCompile(`\bset\b`)
	ctgDoUpdateSet    = regexp.MustCompile(`\bdo update set\b`)
	ctgUpdateSet      = regexp.MustCompile(`\bupdate set\b`)
	ctgSetListEnd     = regexp.MustCompile(`\b(?:where|from|returning)\b`)
	ctgDollarTag      = regexp.MustCompile(`^\$(?:[a-z_][a-z0-9_]*)?\$`)
	ctgTriggerBypass  = regexp.MustCompile(`\b(?:disable|enable replica|enable always) trigger\b`)
	ctgDropTrigger    = regexp.MustCompile(`\bdrop trigger\b`)
	ctgCreateFunction = regexp.MustCompile(`\bcreate (?:or replace )?function (?:public\.)?(?:add_request_identity|add_request_completed)\b`)
)

// ctgTerminals are the GORM calls that write or delete rows.
var ctgTerminals = map[string]bool{
	"Create": true, "CreateInBatches": true, "Save": true, "FirstOrCreate": true, "FirstOrInit": true,
	"Update": true, "UpdateColumn": true, "Updates": true, "UpdateColumns": true, "Delete": true,
}

const ctgResetLiteral = "SET LOCAL session_replication_role = replica;"

// ctgElem is one call in a GORM chain: its method name and arguments.
type ctgElem struct {
	name string
	args []ast.Expr
}

type ctgFile struct {
	path     string
	fset     *token.FileSet
	tree     *ast.File
	models   map[string]bool       // import names of fold/models
	dot      bool                  // fold/models is dot-imported
	inModels bool                  // the file is in package models
	consts   map[*ctgObject]string // same-file string constants by declaration, so a shadowing const is its own
	folded   map[*ast.BinaryExpr]string
	skip     map[*ast.BasicLit]bool
	objs     map[*ctgObject]bool     // declarations the parser tied to ContactRequest values
	single   map[*ctgObject]ast.Expr // locals of the walked declaration assigned exactly once
}

type ctgRun struct {
	bad         []string
	eraseCalls  int
	eraseSites  map[string]int
	agesLiteral bool
	// reasonLiteral, reasonCalls and reasonSites are the same for the reason
	// helper, eraseContactRequestReasonsIn (T07B-2b).
	reasonLiteral bool
	reasonCalls   int
	reasonSites   map[string]int
	// pipelineCalls and pipelineSites count the calls of eraseAccountIn.
	pipelineCalls int
	pipelineSites map[string]int
	resetLiteral  bool
}

// ctgObject is the parser's own resolution of a name to its declaration. It
// is how the guard tells a captured variable from one that shadows it. The
// parser resolves within one file only; a name declared in another file has no
// object and is never bound.
//
//lint:ignore SA1019 Parse-only guard uses parser-local bindings, not type identity; unresolved flows are documented in ctgLimits and AGENTS.md.
type ctgObject = ast.Object

// ctgObj returns the object an identifier declares or refers to, or nil.
func ctgObj(id *ast.Ident) *ctgObject {
	if id == nil {
		return nil
	}
	return id.Obj
}

// ctgDeclaredBy reports whether id is the declaring name of decl itself, as
// opposed to a later assignment to a variable declared elsewhere.
func ctgDeclaredBy(id *ast.Ident, decl ast.Node) bool {
	return id.Obj != nil && id.Obj.Decl == decl
}

func ctgNormalize(s string) string {
	return guardkit.Squash(ctgUnquoteIdents(strings.ToLower(s)))
}

// ctgEscapeString reports whether the single quote at s[i] opens an E-string,
// where a backslash escapes the next byte. s is lower case.
func ctgEscapeString(s string, i int) bool {
	return i > 0 && s[i-1] == 'e' && (i == 1 || strings.IndexByte("abcdefghijklmnopqrstuvwxyz0123456789_", s[i-2]) < 0)
}

// ctgUnquoteIdents drops the double quotes of SQL identifiers so a quoted
// table or column name matches like a bare one. It reads quote context first,
// as ctgStatementEnd does: quoted strings and comments retain their original
// bytes, and a semicolon inside a
// double-quoted identifier (an alias such as "r;kept") becomes an underscore so
// it cannot end the statement later. A double quote that never closes makes the
// rest of s one identifier, so the guard reads more, never less.
func ctgUnquoteIdents(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			for i++; i < len(s); i++ {
				if s[i] == '"' {
					if i+1 < len(s) && s[i+1] == '"' {
						i++
						continue
					}
					break
				}
				if s[i] == ';' {
					b.WriteByte('_')
				} else {
					b.WriteByte(s[i])
				}
			}
		case c == '-' && strings.HasPrefix(s[i:], "--"), c == '/' && strings.HasPrefix(s[i:], "/*"):
			b.WriteString(s[i:])
			return b.String()
		case c == '\'':
			end := ctgQuoteEnd(s, i, ctgEscapeString(s, i))
			if end < 0 {
				end = len(s) - 1
			}
			b.WriteString(s[i : end+1])
			i = end
		case c == '$':
			tag := ctgDollarTag.FindString(s[i:])
			if tag == "" {
				b.WriteByte(c)
				continue
			}
			end := len(s) - 1
			if j := strings.Index(s[i+len(tag):], tag); j >= 0 {
				end = i + len(tag) + j + len(tag) - 1
			}
			b.WriteString(s[i : end+1])
			i = end
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// ctgProtectedName reports whether a column or Go field name (optionally
// quoted or qualified) is reason or add_childcare_ages_note. EndReason and
// end_reason are other names and never match.
func ctgProtectedName(s string) bool {
	s = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\"", ""), "`", "")))
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	return s == "reason" || s == "add_childcare_ages_note" || s == "addchildcareagesnote"
}

func ctgUnwrap(e ast.Expr) ast.Expr {
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
		case *ast.UnaryExpr:
			if x.Op != token.AND {
				return e
			}
			e = x.X
		default:
			return e
		}
	}
}

func ctgTypeName(t ast.Expr) string {
	switch x := t.(type) {
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.Ident:
		return x.Name
	}
	return ""
}

// ctgEnclosing returns the top-level function around the innermost parent and
// whether a func literal sits between them.
func ctgEnclosing(parents []ast.Node) (*ast.FuncDecl, bool) {
	inLit := false
	for i := len(parents) - 1; i >= 0; i-- {
		switch n := parents[i].(type) {
		case *ast.FuncLit:
			inLit = true
		case *ast.FuncDecl:
			return n, inLit
		}
	}
	return nil, inLit
}

// ctgCreationSite reports whether decl is one of the two functions in
// api_contact.go that build a new request. Reason may be set only in the
// composite literal there; the ages note only in apiCreateAddRequest.
func ctgCreationSite(path string, decl *ast.FuncDecl, field string, literal bool) bool {
	if path != "api_contact.go" || decl == nil {
		return false
	}
	switch field {
	case "Reason":
		return literal && (decl.Name.Name == "apiCreateContactRequest" || decl.Name.Name == "apiCreateAddRequest")
	case "AddChildcareAgesNote":
		return decl.Name.Name == "apiCreateAddRequest"
	}
	return false
}

func newCtgFile(path string, fset *token.FileSet, tree *ast.File) *ctgFile {
	f := &ctgFile{
		path: path, fset: fset, tree: tree,
		models:   map[string]bool{},
		inModels: strings.HasPrefix(path, "models/"),
		objs:     map[*ctgObject]bool{},
		single:   map[*ctgObject]ast.Expr{},
		consts:   map[*ctgObject]string{},
	}
	for name := range guardkit.ImportNames(tree, "fold/models") {
		switch name {
		case ".":
			f.dot = true
		case "_":
		default:
			f.models[name] = true
		}
	}
	for pass := 0; pass < 3; pass++ {
		f.collectConsts()
	}
	f.foldLiterals()
	f.bind(tree)
	return f
}

// fold evaluates string literals, same-file string constants and their
// concatenation.
func (f *ctgFile) fold(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return f.fold(x.X)
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(x.Value)
		return s, err == nil
	case *ast.Ident:
		obj := ctgObj(x)
		if obj == nil {
			return "", false
		}
		s, ok := f.consts[obj]
		return s, ok
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		l, ok := f.fold(x.X)
		if !ok {
			return "", false
		}
		r, ok := f.fold(x.Y)
		if !ok {
			return "", false
		}
		return l + r, true
	}
	return "", false
}

func (f *ctgFile) collectConsts() {
	ast.Inspect(f.tree, func(n ast.Node) bool {
		gd, ok := n.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			return true
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != len(vs.Values) {
				continue
			}
			for i, name := range vs.Names {
				if obj := ctgObj(name); obj == nil {
					continue
				} else if s, ok := f.fold(vs.Values[i]); ok {
					f.consts[obj] = s
				}
			}
		}
		return true
	})
}

// foldLiterals finds the maximal constant concatenations. Each is scanned as
// one string; the literals inside it are not scanned again on their own.
func (f *ctgFile) foldLiterals() {
	f.folded = map[*ast.BinaryExpr]string{}
	f.skip = map[*ast.BasicLit]bool{}
	ast.Inspect(f.tree, func(n ast.Node) bool {
		be, ok := n.(*ast.BinaryExpr)
		if !ok || be.Op != token.ADD {
			return true
		}
		s, ok := f.fold(be)
		if !ok {
			return true
		}
		f.folded[be] = s
		ast.Inspect(be, func(m ast.Node) bool {
			if lit, ok := m.(*ast.BasicLit); ok {
				f.skip[lit] = true
			}
			return true
		})
		return false
	})
}

// isCRName: the type expression names models.ContactRequest (through an
// import alias, a dot import, or bare inside package models).
func (f *ctgFile) isCRName(t ast.Expr) bool {
	switch x := t.(type) {
	case *ast.SelectorExpr:
		id, ok := x.X.(*ast.Ident)
		return ok && f.models[id.Name] && x.Sel.Name == "ContactRequest"
	case *ast.Ident:
		return x.Name == "ContactRequest" && (f.dot || f.inModels)
	}
	return false
}

// isCRType also accepts pointers, slices, arrays, maps (by value type) and
// variadic parameters of ContactRequest.
func (f *ctgFile) isCRType(t ast.Expr) bool {
	switch x := t.(type) {
	case *ast.ParenExpr:
		return f.isCRType(x.X)
	case *ast.StarExpr:
		return f.isCRType(x.X)
	case *ast.ArrayType:
		return f.isCRType(x.Elt)
	case *ast.MapType:
		return f.isCRType(x.Value)
	case *ast.Ellipsis:
		return f.isCRType(x.Elt)
	}
	return f.isCRName(t)
}

// bound reports whether the parser can bind the expression to a
// ContactRequest value in this declaration.
func (f *ctgFile) bound(e ast.Expr) bool {
	for steps := 0; steps < 32; steps++ {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		case *ast.UnaryExpr:
			if x.Op != token.AND {
				return false
			}
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.SliceExpr:
			e = x.X
		case *ast.Ident:
			obj := ctgObj(x)
			return obj != nil && f.objs[obj]
		case *ast.CompositeLit:
			return x.Type != nil && f.isCRType(x.Type)
		case *ast.TypeAssertExpr:
			return x.Type != nil && f.isCRType(x.Type)
		case *ast.CallExpr:
			id, ok := x.Fun.(*ast.Ident)
			if !ok || len(x.Args) == 0 {
				return false
			}
			switch id.Name {
			case "new", "make":
				return f.isCRType(x.Args[0])
			case "append":
				e = x.Args[0]
			default:
				return false
			}
		default:
			return false
		}
	}
	return false
}

// crLiteral: a ContactRequest composite literal, including a slice of them.
func (f *ctgFile) crLiteral(e ast.Expr) bool {
	lit, ok := ctgUnwrap(e).(*ast.CompositeLit)
	return ok && lit.Type != nil && f.isCRType(lit.Type)
}

// crValueLiteral: a single ContactRequest literal, by value or pointer.
func (f *ctgFile) crValueLiteral(e ast.Expr) bool {
	lit, ok := ctgUnwrap(e).(*ast.CompositeLit)
	return ok && lit.Type != nil && f.isCRName(lit.Type)
}

// literalsOf returns the ContactRequest literals a composite literal builds:
// itself, or the elided elements of a slice, array or map of them.
func (f *ctgFile) literalsOf(lit *ast.CompositeLit) []*ast.CompositeLit {
	if lit.Type == nil {
		return nil
	}
	return f.literalsFor(lit, lit.Type)
}

func (f *ctgFile) literalsFor(lit *ast.CompositeLit, t ast.Expr) []*ast.CompositeLit {
	for {
		if p, ok := t.(*ast.ParenExpr); ok {
			t = p.X
			continue
		}
		if s, ok := t.(*ast.StarExpr); ok {
			t = s.X
			continue
		}
		break
	}
	if f.isCRName(t) {
		return []*ast.CompositeLit{lit}
	}
	var elem ast.Expr
	switch x := t.(type) {
	case *ast.ArrayType:
		elem = x.Elt
	case *ast.MapType:
		elem = x.Value
	default:
		return nil
	}
	if !f.isCRType(elem) {
		return nil
	}
	var out []*ast.CompositeLit
	for _, el := range lit.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			el = kv.Value
		}
		inner, ok := ctgUnwrap(el).(*ast.CompositeLit)
		if ok && inner.Type == nil {
			out = append(out, f.literalsFor(inner, elem)...)
		}
	}
	return out
}

// bind collects the declarations the parser can tie to ContactRequest values
// in root, to a fixed point so that q := &rs[0] and for _, x := range rs work.
// A declaration is bound, not its spelling: an inner parameter or variable
// that shadows a request's name stays unbound unless it is a request itself.
func (f *ctgFile) bind(root ast.Node) {
	for pass := 0; pass < 4; pass++ {
		changed := false
		set := func(id *ast.Ident) {
			if obj := ctgObj(id); obj != nil && id.Name != "_" && !f.objs[obj] {
				f.objs[obj] = true
				changed = true
			}
		}
		fields := func(list *ast.FieldList) {
			if list == nil {
				return
			}
			for _, fld := range list.List {
				if f.isCRType(fld.Type) {
					for _, id := range fld.Names {
						set(id)
					}
				}
			}
		}
		ast.Inspect(root, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				fields(x.Recv)
			case *ast.FuncType:
				fields(x.Params)
				fields(x.Results)
			case *ast.ValueSpec:
				if x.Type != nil {
					if f.isCRType(x.Type) {
						for _, id := range x.Names {
							set(id)
						}
					}
				} else if len(x.Values) == len(x.Names) {
					for i, id := range x.Names {
						if f.bound(x.Values[i]) {
							set(id)
						}
					}
				}
			case *ast.AssignStmt:
				if len(x.Lhs) == len(x.Rhs) {
					for i, lhs := range x.Lhs {
						if id, ok := lhs.(*ast.Ident); ok && f.bound(x.Rhs[i]) {
							set(id)
						}
					}
				}
			case *ast.RangeStmt:
				if id, ok := x.Value.(*ast.Ident); ok && f.bound(x.X) {
					set(id)
				}
			}
			return true
		})
		if !changed {
			break
		}
	}
}

// singleAssigned records the locals of decl that are declared in it and
// assigned exactly once, so q := tx.Model(&r).Where(...) followed by
// q.Update(...) is one chain. Variables are the parser's objects: a closure
// that captures q is the same variable and its assignments count against it,
// while a q declared inside a closure is another variable. Parameters, range
// variables, op-assignments and &q count twice, which takes them out.
func (f *ctgFile) singleAssigned(decl ast.Node) {
	count := map[*ctgObject]int{}
	value := map[*ctgObject]ast.Expr{}
	declared := map[*ctgObject]bool{}
	heavy := func(e ast.Expr) {
		if obj := ctgObj(ctgIdent(guardkit.Unparen(e))); obj != nil {
			count[obj] += 2
		}
	}
	ast.Inspect(decl, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range x.Lhs {
				id := ctgIdent(guardkit.Unparen(lhs))
				obj := ctgObj(id)
				if obj == nil {
					continue
				}
				count[obj]++
				if x.Tok != token.ASSIGN && x.Tok != token.DEFINE {
					count[obj] += 2
				}
				if ctgDeclaredBy(id, x) {
					declared[obj] = true
				}
				if len(x.Lhs) == len(x.Rhs) {
					value[obj] = x.Rhs[i]
				}
			}
		case *ast.ValueSpec:
			for i, id := range x.Names {
				obj := ctgObj(id)
				if obj == nil {
					continue
				}
				declared[obj] = true
				if len(x.Values) > 0 {
					count[obj]++
					if len(x.Names) == len(x.Values) {
						value[obj] = x.Values[i]
					}
				}
			}
		case *ast.RangeStmt:
			for _, e := range []ast.Expr{x.Key, x.Value} {
				if e != nil {
					heavy(e)
				}
			}
		case *ast.UnaryExpr:
			if x.Op == token.AND {
				heavy(x.X)
			}
		case *ast.FuncType:
			for _, list := range []*ast.FieldList{x.Params, x.Results} {
				if list == nil {
					continue
				}
				for _, fld := range list.List {
					for _, id := range fld.Names {
						if obj := ctgObj(id); obj != nil {
							count[obj] += 2
						}
					}
				}
			}
		}
		return true
	})
	f.single = map[*ctgObject]ast.Expr{}
	for obj, c := range count {
		if v := value[obj]; c == 1 && v != nil && declared[obj] {
			f.single[obj] = v
		}
	}
}

// ctgIdent returns e as an identifier, or nil.
func ctgIdent(e ast.Expr) *ast.Ident {
	id, _ := e.(*ast.Ident)
	return id
}

// chain reads the receiver chain that ends in call, terminal first.
func (f *ctgFile) chain(call *ast.CallExpr) []ctgElem {
	var elems []ctgElem
	seen := map[*ctgObject]bool{}
	var cur ast.Expr = call
	for step := 0; step < 64; step++ {
		switch x := cur.(type) {
		case *ast.ParenExpr:
			cur = x.X
		case *ast.CallExpr:
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok {
				return elems
			}
			elems = append(elems, ctgElem{sel.Sel.Name, x.Args})
			cur = sel.X
		case *ast.Ident:
			obj := ctgObj(x)
			next, ok := f.single[obj]
			if !ok || seen[obj] {
				return elems
			}
			seen[obj] = true
			cur = next
		default:
			return elems
		}
	}
	return elems
}

// litKeysProblem reads the keys of a map or struct literal. It returns "" when
// every key is readable and none names a protected column.
func (f *ctgFile) litKeysProblem(lit *ast.CompositeLit) string {
	_, isMap := lit.Type.(*ast.MapType)
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			return "an element without a key, which the guard cannot read"
		}
		var name string
		switch k := kv.Key.(type) {
		case *ast.BasicLit:
			s, err := strconv.Unquote(k.Value)
			if k.Kind != token.STRING || err != nil {
				return "a key the guard cannot read"
			}
			name = s
		case *ast.Ident:
			if !isMap {
				name = k.Name
			} else if s, ok := f.consts[ctgObj(k)]; ok {
				name = s
			} else {
				return "a map key held in a variable, which the guard cannot read"
			}
		default:
			return "a key the guard cannot read"
		}
		if ctgProtectedName(name) {
			return "a key naming reason or add_childcare_ages_note"
		}
	}
	return ""
}

// updatesProblem: the argument of Updates or UpdateColumns must be a literal
// whose keys the guard can read and none of which is a protected column.
func (f *ctgFile) updatesProblem(arg ast.Expr) string {
	lit, ok := ctgUnwrap(arg).(*ast.CompositeLit)
	if !ok {
		return "an Updates argument that is not a literal, so the guard cannot show it leaves reason and the ages note out"
	}
	return f.litKeysProblem(lit)
}

// conditionProblem reads a Where, Assign, Attrs or inline FirstOrCreate
// condition.
func (f *ctgFile) conditionProblem(arg ast.Expr) string {
	e := ctgUnwrap(arg)
	if s, ok := f.fold(e); ok {
		if ctgProtectedWord.MatchString(s) {
			return "a condition naming reason or add_childcare_ages_note"
		}
		return ""
	}
	if lit, ok := e.(*ast.CompositeLit); ok {
		return f.litKeysProblem(lit)
	}
	return "a condition that is not a literal the guard can read"
}

func (f *ctgFile) selectProblem(args []ast.Expr) string {
	const bad = "Select names reason, add_childcare_ages_note or *, or cannot be read, before a write"
	for _, a := range args {
		e := ctgUnwrap(a)
		if s, ok := f.fold(e); ok {
			if strings.Contains(s, "*") || ctgProtectedWord.MatchString(s) {
				return bad
			}
			continue
		}
		lit, ok := e.(*ast.CompositeLit)
		if !ok {
			return bad
		}
		for _, el := range lit.Elts {
			s, ok := f.fold(el)
			if !ok || strings.Contains(s, "*") || ctgProtectedWord.MatchString(s) {
				return bad
			}
		}
	}
	return ""
}

func (f *ctgFile) doUpdatesProblem(v ast.Expr) string {
	call, ok := ctgUnwrap(v).(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return "an OnConflict DoUpdates that is not a call the guard can read"
	}
	switch ctgTypeName(call.Fun) {
	case "AssignmentColumns":
		lit, ok := ctgUnwrap(call.Args[0]).(*ast.CompositeLit)
		if !ok {
			return "AssignmentColumns with a list the guard cannot read"
		}
		for _, el := range lit.Elts {
			s, ok := f.fold(el)
			if !ok {
				return "AssignmentColumns with a name the guard cannot read"
			}
			if ctgProtectedName(s) {
				return "an OnConflict DoUpdates naming reason or add_childcare_ages_note"
			}
		}
		return ""
	case "Assignments":
		lit, ok := ctgUnwrap(call.Args[0]).(*ast.CompositeLit)
		if !ok {
			return "Assignments with a map the guard cannot read"
		}
		return f.litKeysProblem(lit)
	}
	return "an OnConflict DoUpdates that is neither clause.AssignmentColumns nor clause.Assignments"
}

// conflictProblem reads one Clauses argument on a contact_requests target. A
// literal of another clause type (Locking, Where) is not a conflict clause.
func (f *ctgFile) conflictProblem(arg ast.Expr) string {
	lit, ok := ctgUnwrap(arg).(*ast.CompositeLit)
	if !ok {
		return "a Clauses argument that is not a literal, so the guard cannot read UpdateAll or DoUpdates"
	}
	if ctgTypeName(lit.Type) != "OnConflict" {
		return ""
	}
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			return "an OnConflict literal without field names"
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch key.Name {
		case "UpdateAll":
			if v, ok := kv.Value.(*ast.Ident); !ok || v.Name != "false" {
				return "OnConflict UpdateAll writes every non-key column, including reason and the ages note"
			}
		case "DoUpdates":
			if why := f.doUpdatesProblem(kv.Value); why != "" {
				return why
			}
		}
	}
	return ""
}

func (run *ctgRun) add(f *ctgFile, rule string, n ast.Node, parents []ast.Node, format string, args ...any) {
	where := "package level"
	if decl, _ := ctgEnclosing(parents); decl != nil {
		where = decl.Name.Name
	}
	run.bad = append(run.bad, fmt.Sprintf("%s:%d: %s: %s: %s", f.path, f.fset.Position(n.Pos()).Line, where, rule, fmt.Sprintf(format, args...)))
}

// imports is RC-1 (d): production code never imports internal/testdb, whose
// reset literal is the one session_replication_role allowance.
func (run *ctgRun) imports(f *ctgFile) {
	if strings.HasPrefix(f.path, "internal/testdb/") {
		return
	}
	for _, imp := range f.tree.Imports {
		if p, err := strconv.Unquote(imp.Path.Value); err == nil && p == "fold/internal/testdb" {
			run.bad = append(run.bad, fmt.Sprintf("%s:%d: package level: RC-1(d): production code imports fold/internal/testdb, whose reset sets session_replication_role (plain, aliased, blank and dot imports are all rejected)", f.path, f.fset.Position(imp.Pos()).Line))
		}
	}
}

func (run *ctgRun) file(f *ctgFile) {
	for _, decl := range f.tree.Decls {
		f.singleAssigned(decl)
		run.walk(f, decl)
	}
}

func (run *ctgRun) walk(f *ctgFile, root ast.Node) {
	guardkit.InspectStack(root, func(n ast.Node, parents []ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			run.assign(f, x, parents)
		case *ast.CompositeLit:
			run.literal(f, x, parents)
		case *ast.CallExpr:
			run.call(f, x, parents)
		case *ast.BasicLit:
			if x.Kind == token.STRING && !f.skip[x] {
				if s, err := strconv.Unquote(x.Value); err == nil {
					run.text(f, x, s, parents)
				}
			}
		case *ast.BinaryExpr:
			if s, ok := f.folded[x]; ok {
				run.text(f, x, s, parents)
			}
		case *ast.Ident:
			run.ident(f, x, parents)
		}
		return true
	})
}

// assign is R-1 (field writes) and the overwrite half of R-2.
func (run *ctgRun) assign(f *ctgFile, a *ast.AssignStmt, parents []ast.Node) {
	decl, _ := ctgEnclosing(parents)
	for i, lhs := range a.Lhs {
		if sel, ok := guardkit.Unparen(lhs).(*ast.SelectorExpr); ok && (sel.Sel.Name == "Reason" || sel.Sel.Name == "AddChildcareAgesNote") && f.bound(sel.X) {
			if !ctgCreationSite(f.path, decl, sel.Sel.Name, false) {
				run.add(f, "R-1", a, parents, "assignment to %s on a models.ContactRequest value outside its allowed site", sel.Sel.Name)
			}
		}
		if a.Tok != token.ASSIGN || len(a.Lhs) != len(a.Rhs) || !f.crValueLiteral(a.Rhs[i]) {
			continue
		}
		overwrite := false
		switch t := ctgUnwrap(lhs).(type) {
		case *ast.StarExpr:
			overwrite = f.bound(t.X)
		case *ast.IndexExpr:
			overwrite = f.bound(t.X)
		case *ast.Ident:
			// Rebinding a pointer variable to a fresh literal overwrites no row.
			overwrite = f.bound(t) && !ctgIsPointerLiteral(a.Rhs[i])
		}
		if overwrite && !ctgCreationSite(f.path, decl, "Reason", true) {
			run.add(f, "R-2", a, parents, "a models.ContactRequest literal assigned over an existing value; a literal that omits Reason silently blanks it")
		}
	}
}

func ctgIsPointerLiteral(e ast.Expr) bool {
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
		case *ast.UnaryExpr:
			return x.Op == token.AND
		default:
			return false
		}
	}
}

// literal is R-2 (composite literals that set a protected field).
func (run *ctgRun) literal(f *ctgFile, lit *ast.CompositeLit, parents []ast.Node) {
	decl, _ := ctgEnclosing(parents)
	for _, cr := range f.literalsOf(lit) {
		for _, el := range cr.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				run.add(f, "R-2", cr, parents, "a positional models.ContactRequest literal sets every field, including Reason")
				break
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || (key.Name != "Reason" && key.Name != "AddChildcareAgesNote") {
				continue
			}
			if !ctgCreationSite(f.path, decl, key.Name, true) {
				run.add(f, "R-2", cr, parents, "models.ContactRequest literal sets %s outside its allowed creation site", key.Name)
			}
		}
	}
}

// call applies R-2 (Save of a literal), R-3, R-4a, R-4b, R-4c and R-7 to a
// GORM chain that has a contact_requests target.
func (run *ctgRun) call(f *ctgFile, call *ast.CallExpr, parents []ast.Node) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !ctgTerminals[sel.Sel.Name] {
		return
	}
	name := sel.Sel.Name
	elems := f.chain(call)
	var first ast.Expr
	if len(call.Args) > 0 {
		first = call.Args[0]
	}
	if name == "Save" && first != nil && f.crLiteral(first) {
		run.add(f, "R-2", call, parents, "Save of a models.ContactRequest composite literal; save only a request loaded from the database")
	}
	explicit, hasModel := false, false
	for _, e := range elems {
		switch e.name {
		case "Model":
			hasModel = true
			if len(e.args) > 0 && f.bound(e.args[0]) {
				explicit = true
			}
		case "Table":
			if len(e.args) > 0 {
				if s, ok := f.fold(e.args[0]); ok && ctgTableName.MatchString(ctgNormalize(s)) {
					explicit = true
				}
			}
		}
	}
	implicit := false
	if first != nil && f.bound(first) {
		switch name {
		case "Create", "CreateInBatches", "Save", "FirstOrCreate", "FirstOrInit", "Delete":
			implicit = true
		case "Updates", "UpdateColumns":
			implicit = !hasModel
		}
	}
	if !explicit && !implicit {
		return
	}
	if name == "Delete" {
		run.add(f, "R-7", call, parents, "GORM Delete on a contact_requests target; the erasure authority deletes no request row")
		return
	}
	findOrCreate := name == "FirstOrCreate" || name == "FirstOrInit"
	for _, e := range elems {
		switch e.name {
		case "Clauses":
			for _, a := range e.args {
				if why := f.conflictProblem(a); why != "" {
					run.add(f, "R-4a", call, parents, "%s", why)
				}
			}
		case "Select":
			if name == "Save" || strings.HasPrefix(name, "Update") {
				if why := f.selectProblem(e.args); why != "" {
					run.add(f, "R-4c", call, parents, "%s", why)
				}
			}
		case "Assign", "Attrs":
			if findOrCreate {
				for _, a := range e.args {
					if why := f.conditionProblem(a); why != "" {
						run.add(f, "R-4b", call, parents, "%s in %s of %s", why, e.name, name)
					}
				}
			}
		case "Where":
			if findOrCreate && len(e.args) > 0 {
				if why := f.conditionProblem(e.args[0]); why != "" {
					run.add(f, "R-4b", call, parents, "%s in Where of %s", why, name)
				}
			}
		}
	}
	if findOrCreate && len(call.Args) > 1 {
		if why := f.conditionProblem(call.Args[1]); why != "" {
			run.add(f, "R-4b", call, parents, "%s as an inline condition of %s", why, name)
		}
	}
	switch name {
	case "Update", "UpdateColumn":
		s, ok := f.fold(first)
		if !ok || ctgProtectedName(s) {
			run.add(f, "R-3", call, parents, "%s names a column the guard cannot show is neither reason nor add_childcare_ages_note", name)
		}
	case "Updates", "UpdateColumns":
		if why := f.updatesProblem(first); why != "" {
			run.add(f, "R-3", call, parents, "%s: %s", name, why)
		}
	}
}

type ctgWrite struct{ kind, list string }

// ctgStatementEnd returns where the SQL statement at the front of s ends: the
// first semicolon outside a quoted string. It reads single-quoted strings
// (a doubled quote stays inside; after an E prefix a backslash escapes the next
// byte) and dollar quotes. A comment opener or a quote that never closes makes
// the rest of s one statement, so the guard reads more, never less. Double-quoted
// identifiers are handled earlier, by ctgUnquoteIdents, which hides their
// semicolons; s never holds a double quote.
func ctgStatementEnd(s string) int {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ';':
			return i
		case '-':
			if strings.HasPrefix(s[i:], "--") {
				return len(s)
			}
		case '/':
			if strings.HasPrefix(s[i:], "/*") {
				return len(s)
			}
		case '\'':
			end := ctgQuoteEnd(s, i, ctgEscapeString(s, i))
			if end < 0 {
				return len(s)
			}
			i = end
		case '$':
			if tag := ctgDollarTag.FindString(s[i:]); tag != "" {
				j := strings.Index(s[i+len(tag):], tag)
				if j < 0 {
					return len(s)
				}
				i += len(tag) + j + len(tag) - 1
			}
		}
	}
	return len(s)
}

// ctgQuoteEnd returns the index of the quote that closes the string opened at
// s[open], or -1 when it never closes.
func ctgQuoteEnd(s string, open int, escapes bool) int {
	for j := open + 1; j < len(s); j++ {
		if escapes && s[j] == '\\' {
			j++
			continue
		}
		if s[j] != '\'' {
			continue
		}
		if j+1 < len(s) && s[j+1] == '\'' {
			j++
			continue
		}
		return j
	}
	return -1
}

// ctgSQLWrites finds the statements in normalized SQL that update a
// contact_requests row and mention a protected column after SET.
func ctgSQLWrites(t string) []ctgWrite {
	var out []ctgWrite
	scan := func(start, after *regexp.Regexp, kind string) {
		for _, m := range start.FindAllStringIndex(t, -1) {
			rest := t[m[1]:]
			rest = rest[:ctgStatementEnd(rest)]
			loc := after.FindStringIndex(rest)
			if loc == nil {
				continue
			}
			list := rest[loc[1]:]
			if ctgProtectedWord.MatchString(list) {
				out = append(out, ctgWrite{kind, list})
			}
		}
	}
	scan(ctgUpdateSQL, ctgSetKeyword, "UPDATE")
	scan(ctgInsertSQL, ctgDoUpdateSet, "INSERT ... ON CONFLICT DO UPDATE")
	scan(ctgMergeSQL, ctgUpdateSet, "MERGE ... UPDATE SET")
	return out
}

func ctgSetPart(list string) string {
	if loc := ctgSetListEnd.FindStringIndex(list); loc != nil {
		list = list[:loc[0]]
	}
	return strings.TrimSpace(list)
}

// resetAllowed is the one RC-1 (b) allowance: the exact reset literal as the
// argument of b.WriteString, directly in resetStatements of
// internal/testdb/testdb.go, once.
func (run *ctgRun) resetAllowed(f *ctgFile, n ast.Node, raw string, parents []ast.Node) bool {
	lit, ok := n.(*ast.BasicLit)
	if !ok || raw != ctgResetLiteral || f.path != "internal/testdb/testdb.go" || run.resetLiteral || len(parents) < 2 {
		return false
	}
	decl, inLit := ctgEnclosing(parents)
	if decl == nil || decl.Recv != nil || decl.Name.Name != "resetStatements" || inLit {
		return false
	}
	call, ok := parents[len(parents)-2].(*ast.CallExpr)
	if !ok || len(call.Args) != 1 || call.Args[0] != ast.Expr(lit) {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "WriteString" {
		return false
	}
	recv, ok := sel.X.(*ast.Ident)
	return ok && recv.Name == "b"
}

// text applies the string-literal rules: R-5 (raw SQL writes), R-7 (raw row
// deletion) and RC-1 (b) (trigger and replication-role bypasses).
func (run *ctgRun) text(f *ctgFile, n ast.Node, raw string, parents []ast.Node) {
	t := ctgNormalize(raw)
	if strings.Contains(t, "session_replication_role") {
		if run.resetAllowed(f, n, raw, parents) {
			run.resetLiteral = true
		} else {
			run.add(f, "RC-1(b)", n, parents, "session_replication_role in a Go string literal; the only allowance is the exact b.WriteString argument in resetStatements of internal/testdb/testdb.go")
		}
	}
	if ctgTriggerBypass.MatchString(t) {
		run.add(f, "RC-1(b)", n, parents, "DISABLE TRIGGER, ENABLE REPLICA TRIGGER or ENABLE ALWAYS TRIGGER in a Go string literal; both triggers must stay enabled in origin mode")
	}
	if ctgDropTrigger.MatchString(t) {
		run.add(f, "RC-1(b)", n, parents, "DROP TRIGGER in a Go string literal")
	}
	if ctgCreateFunction.MatchString(t) {
		run.add(f, "RC-1(b)", n, parents, "CREATE FUNCTION for add_request_identity or add_request_completed in a Go string literal; only a migration defines them")
	}
	if ctgDeleteSQL.MatchString(t) || ctgTruncateSQL.MatchString(t) {
		run.add(f, "R-7", n, parents, "raw SQL deletes or truncates contact_requests; the erasure authority deletes no request row")
	}
	writes := ctgSQLWrites(t)
	if len(writes) == 0 {
		return
	}
	only := len(writes) == 1 && writes[0].kind == "UPDATE" && ctgSetPart(writes[0].list) == "add_childcare_ages_note = ''"
	onlyReason := len(writes) == 1 && writes[0].kind == "UPDATE" && ctgSetPart(writes[0].list) == "reason = ''"
	decl, inLit := ctgEnclosing(parents)
	site := f.path == "account_erasure.go" && decl != nil && decl.Recv == nil && decl.Name.Name == "eraseContactRequestTextIn" && !inLit && !run.agesLiteral
	if only && site {
		run.agesLiteral = true
		return
	}
	reasonSite := f.path == "account_erasure_reasons.go" && decl != nil && decl.Recv == nil && decl.Name.Name == "eraseContactRequestReasonsIn" && !inLit && !run.reasonLiteral
	if onlyReason && reasonSite {
		run.reasonLiteral = true
		return
	}
	run.add(f, "R-5", n, parents, "raw SQL writes reason or add_childcare_ages_note on contact_requests (%s); the only allowances are the single UPDATE literal in eraseContactRequestTextIn (account_erasure.go) whose SET list is add_childcare_ages_note = '' and the single one in eraseContactRequestReasonsIn (account_erasure_reasons.go) whose SET list is reason = ''", writes[0].kind)
}

// ident is R-6. The two scrubs, eraseContactRequestTextIn and
// eraseContactRequestReasonsIn, are each called exactly once, directly in the
// body of eraseAccountIn (account_erasure.go), after its soft delete; and
// eraseAccountIn is called exactly twice, directly in the admin deletion's
// a.fencedContactMutation callback and in the tombstone sweep's
// a.contactMutationCtx callback. A use as a value is never allowed.
func (run *ctgRun) ident(f *ctgFile, id *ast.Ident, parents []ast.Node) {
	reasons := id.Name == "eraseContactRequestReasonsIn"
	pipeline := id.Name == ctgPipelineName
	if (id.Name != "eraseContactRequestTextIn" && !reasons && !pipeline) || len(parents) < 2 {
		return
	}
	parent := parents[len(parents)-2]
	if fd, ok := parent.(*ast.FuncDecl); ok && fd.Name == id {
		return
	}
	if pipeline {
		// a.eraseAccountIn(...): the selector's name, called.
		sel, ok := parent.(*ast.SelectorExpr)
		if !ok || sel.Sel != id || len(parents) < 3 {
			run.add(f, "R-6", id, parents, "%s used other than as a method call on the App receiver", id.Name)
			return
		}
		call, ok := parents[len(parents)-3].(*ast.CallExpr)
		if !ok || call.Fun != ast.Expr(sel) {
			run.add(f, "R-6", id, parents, "%s used as a value (a method value, passed, returned or stored); only a direct call is allowed", id.Name)
			return
		}
		run.pipelineCalls++
		if run.pipelineSites == nil {
			run.pipelineSites = map[string]int{}
		}
		run.pipelineSites[f.path]++
		if why := ctgPipelineCallProblem(f, parents[:len(parents)-1], call, sel); why != "" {
			run.add(f, "R-6", id, parents, "%s", why)
		}
		return
	}
	if p, ok := parent.(*ast.CallExpr); ok && p.Fun == ast.Expr(id) {
		if reasons {
			run.reasonCalls++
			if run.reasonSites == nil {
				run.reasonSites = map[string]int{}
			}
			run.reasonSites[f.path]++
		} else {
			run.eraseCalls++
			if run.eraseSites == nil {
				run.eraseSites = map[string]int{}
			}
			run.eraseSites[f.path]++
		}
		if why := ctgScrubCallProblem(f, parents, p, reasons); why != "" {
			run.add(f, "R-6", id, parents, "%s", why)
		}
		return
	}
	run.add(f, "R-6", id, parents, "%s used as a value (assigned, passed, returned, stored or turned into a method value); only a direct call is allowed", id.Name)
}

// ctgPipelineName and ctgPipelineFile name the one erasure pipeline.
const (
	ctgPipelineName = "eraseAccountIn"
	ctgPipelineFile = "account_erasure.go"
)

// ctgStatementOf finds the statement that holds the call at parents[ci]: an
// expression statement, an assignment, or the init or condition of an if. It
// returns its index in parents, or -1 and why the call sits elsewhere. stop is
// the index of the body the statement must sit in.
func ctgStatementOf(parents []ast.Node, ci, stop int) (int, string) {
	viaExpr := false
	stmt := -1
	for i := ci - 1; i > stop && stmt < 0; i-- {
		switch parents[i].(type) {
		case *ast.ParenExpr:
		case *ast.BinaryExpr, *ast.UnaryExpr:
			viaExpr = true
		case *ast.ExprStmt, *ast.AssignStmt:
			if viaExpr {
				return -1, "call must be a whole expression statement, an assignment or an if init or condition"
			}
			stmt = i
		case *ast.IfStmt:
			stmt = i
		default:
			return -1, "call must sit directly in the body as an expression statement, an assignment or the init or condition of an if statement"
		}
	}
	if stmt > 0 {
		if is, ok := parents[stmt-1].(*ast.IfStmt); ok && is.Init != nil && ast.Node(is.Init) == parents[stmt] {
			stmt--
		}
	}
	return stmt, ""
}

// ctgScrubCallProblem checks one call of a scrub against R-6. parents ends
// with the identifier; the call is its parent.
func ctgScrubCallProblem(f *ctgFile, parents []ast.Node, call *ast.CallExpr, reasons bool) string {
	ci := len(parents) - 2
	di := -1
	for i := ci - 1; i >= 0 && di < 0; i-- {
		switch parents[i].(type) {
		case *ast.GoStmt, *ast.DeferStmt:
			return "called in a go or defer statement"
		case *ast.FuncLit:
			return "called in a func literal (a nested transaction body or a closure), not directly in " + ctgPipelineName
		case *ast.FuncDecl:
			di = i
		}
	}
	if di < 0 {
		return "called outside a function"
	}
	decl := parents[di].(*ast.FuncDecl)
	if decl.Name.Name != ctgPipelineName || f.path != ctgPipelineFile || !ctgAppMethod(decl) {
		return "called from a function other than the App method " + ctgPipelineName + " in " + ctgPipelineFile
	}
	stmt, why := ctgStatementOf(parents, ci, di+1)
	if why != "" {
		return why
	}
	if stmt != di+2 || parents[di+1] != ast.Node(decl.Body) {
		return "call must be a statement of " + ctgPipelineName + "'s body itself, not nested in a block"
	}
	param := ctgFirstParam(decl)
	if arg, ok := ctgFirstArg(call).(*ast.Ident); !ok || param == nil || ctgObj(arg) != param {
		return "the first argument must be " + ctgPipelineName + "'s own *gorm.DB parameter, not a local that reuses its name"
	}
	callAt, deleteAt := -1, -1
	for i, s := range decl.Body.List {
		if ast.Node(s) == parents[stmt] {
			callAt = i
		}
		if deleteAt < 0 && ctgSoftDelete(decl, param, s) {
			deleteAt = i
		}
	}
	if deleteAt < 0 {
		return "no statement holding tx.Delete(opts.Live.User) (the subject's soft delete) in " + ctgPipelineName + " to come after"
	}
	if callAt <= deleteAt {
		return "call must come after the statement holding the subject's soft delete"
	}
	if reasons {
		// Directly after the ages-note scrub: the statement before it holds the
		// eraseContactRequestTextIn call.
		prev := callAt - 1
		// An error check on a separately assigned result sits between them.
		if prev > 0 && !ctgHasCall(decl.Body.List[prev], "eraseContactRequestTextIn") {
			if is, ok := decl.Body.List[prev].(*ast.IfStmt); ok && is.Init == nil {
				prev--
			}
		}
		if prev < 0 || !ctgHasCall(decl.Body.List[prev], "eraseContactRequestTextIn") {
			return "call must be the statement directly after the eraseContactRequestTextIn call"
		}
	}
	return ""
}

// ctgSoftDelete reports whether s holds param.Delete(opts.Live.User), where
// param is the function's own transaction and opts one of its parameters.
func ctgSoftDelete(decl *ast.FuncDecl, param *ctgObject, s ast.Stmt) bool {
	found := false
	ast.Inspect(s, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok || len(c.Args) != 1 {
			return true
		}
		dsel, ok := c.Fun.(*ast.SelectorExpr)
		if !ok || dsel.Sel.Name != "Delete" {
			return true
		}
		recv, ok := dsel.X.(*ast.Ident)
		if !ok || ctgObj(recv) != param {
			return true
		}
		user, ok := c.Args[0].(*ast.SelectorExpr)
		if !ok || user.Sel.Name != "User" {
			return true
		}
		live, ok := user.X.(*ast.SelectorExpr)
		if !ok || live.Sel.Name != "Live" {
			return true
		}
		opts, ok := live.X.(*ast.Ident)
		if ok && ctgObj(opts) != nil && ctgIsParam(decl, ctgObj(opts)) {
			found = true
		}
		return true
	})
	return found
}

func ctgIsParam(decl *ast.FuncDecl, obj *ctgObject) bool {
	for _, fld := range decl.Type.Params.List {
		for _, n := range fld.Names {
			if ctgObj(n) == obj {
				return true
			}
		}
	}
	return false
}

func ctgFirstParam(decl *ast.FuncDecl) *ctgObject {
	if decl.Type.Params == nil || len(decl.Type.Params.List) == 0 || len(decl.Type.Params.List[0].Names) == 0 {
		return nil
	}
	n := decl.Type.Params.List[0].Names[0]
	if n.Name == "_" {
		return nil
	}
	return ctgObj(n)
}

// ctgAppMethod reports whether decl is a method on App or *App.
func ctgAppMethod(decl *ast.FuncDecl) bool {
	if decl.Recv == nil || len(decl.Recv.List) != 1 || len(decl.Recv.List[0].Names) != 1 {
		return false
	}
	t := guardkit.Unparen(decl.Recv.List[0].Type)
	if star, ok := t.(*ast.StarExpr); ok {
		t = guardkit.Unparen(star.X)
	}
	id, ok := t.(*ast.Ident)
	return ok && id.Name == "App"
}

// ctgPipelineCallProblem checks one call of eraseAccountIn against R-6.
// parents ends with the selector; the call is its parent.
func ctgPipelineCallProblem(f *ctgFile, parents []ast.Node, call *ast.CallExpr, sel *ast.SelectorExpr) string {
	path := f.path
	ci := len(parents) - 2
	lit := -1
	for i := ci - 1; i >= 0 && lit < 0; i-- {
		switch parents[i].(type) {
		case *ast.GoStmt, *ast.DeferStmt:
			return "called in a go or defer statement"
		case *ast.FuncLit:
			lit = i
		}
	}
	if lit < 1 {
		return "called outside an erasure's transaction callback"
	}
	fl, ok := parents[lit].(*ast.FuncLit)
	if !ok {
		return "called outside an erasure's transaction callback"
	}
	outer, ok := parents[lit-1].(*ast.CallExpr)
	if !ok {
		return "called in a func literal that is not an erasure's transaction callback"
	}
	osel, ok := outer.Fun.(*ast.SelectorExpr)
	isArg := len(outer.Args) > 0 && outer.Args[len(outer.Args)-1] == ast.Expr(fl)
	if !ok || (osel.Sel.Name != "contactMutationCtx" && osel.Sel.Name != "fencedContactMutation") || !isArg {
		return "called in a func literal that is not the last argument of a.fencedContactMutation or a.contactMutationCtx (a nested tx.Transaction body and the old a.contactMutation are not allowed)"
	}
	var decl *ast.FuncDecl
	for i := lit - 2; i >= 0 && decl == nil; i-- {
		switch n := parents[i].(type) {
		case *ast.FuncLit:
			return "the transaction call is itself inside a func literal"
		case *ast.FuncDecl:
			decl = n
		}
	}
	admin := decl != nil && decl.Name.Name == "apiAdminDeleteUser" && path == "api_admin.go"
	tombstone := decl != nil && decl.Name.Name == "eraseTombstoneOnce" && path == "account_erasure_tombstones.go"
	if !admin && !tombstone {
		return "called from a function other than apiAdminDeleteUser in api_admin.go or eraseTombstoneOnce in account_erasure_tombstones.go"
	}
	if admin && osel.Sel.Name != "fencedContactMutation" {
		return "the admin deletion's callback must be the last argument of a.fencedContactMutation"
	}
	if tombstone && osel.Sel.Name != "contactMutationCtx" {
		return "the tombstone callback must be the last argument of a.contactMutationCtx"
	}
	if !ctgIsAppReceiver(decl, osel.X) {
		return "the callback must be passed on the allowed method's own App receiver, not on another value that has a method of that name"
	}
	if !ctgIsAppReceiver(decl, sel.X) {
		return ctgPipelineName + " must be called on the allowed method's own App receiver"
	}
	stmt, why := ctgStatementOf(parents, ci, lit)
	if why != "" {
		return why
	}
	if stmt != lit+2 || parents[lit+1] != ast.Node(fl.Body) {
		return "call must be a statement of the callback body itself, not nested in a block"
	}
	if fl.Type.Params == nil || len(fl.Type.Params.List) != 1 || len(fl.Type.Params.List[0].Names) != 1 {
		return "the callback must take one named *gorm.DB parameter"
	}
	param := ctgObj(fl.Type.Params.List[0].Names[0])
	if arg, ok := ctgFirstArg(call).(*ast.Ident); !ok || param == nil || ctgObj(arg) != param {
		return "the first argument must be the callback's own *gorm.DB parameter, not a local that reuses its name"
	}
	if tombstone {
		return ctgTombstoneLockProblem(f, fl, param, parents[stmt])
	}
	return ""
}

// ctgHasCall reports whether n contains a call of the package function name.
func ctgHasCall(n ast.Node, name string) bool {
	found := false
	ast.Inspect(n, func(m ast.Node) bool {
		if c, ok := m.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok && id.Name == name {
				found = true
			}
		}
		return true
	})
	return found
}

// The tombstone allowance requires the direct lock assignment and both
// refusal checks before the scrub. This is a syntactic check of the current
// production shape, not a claim to prove arbitrary control flow.
func ctgTombstoneLockProblem(f *ctgFile, fl *ast.FuncLit, param *ctgObject, scrub ast.Node) string {
	for i, stmt := range fl.Body.List {
		if ast.Node(stmt) == scrub {
			break
		}
		assign, ok := stmt.(*ast.AssignStmt)
		if !ok || assign.Tok != token.DEFINE || len(assign.Lhs) != 2 || len(assign.Rhs) != 1 {
			continue
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			continue
		}
		tx, txOK := call.Args[0].(*ast.Ident)
		if !ctgPackageIdent(f, call.Fun, "lockTombstoneIn") || !txOK || ctgObj(tx) != param {
			continue
		}
		found, foundOK := assign.Lhs[0].(*ast.Ident)
		err, errOK := assign.Lhs[1].(*ast.Ident)
		if !foundOK || !errOK || ctgObj(found) == nil || ctgObj(err) == nil || i+3 >= len(fl.Body.List) {
			continue
		}
		// Neither a nested check nor an unchecked lock grants this allowance.
		errCheck, ok := fl.Body.List[i+1].(*ast.IfStmt)
		if !ok || errCheck.Init != nil || errCheck.Else != nil {
			continue
		}
		cond, ok := errCheck.Cond.(*ast.BinaryExpr)
		if !ok || cond.Op != token.NEQ || ctgObj(ctgIdent(cond.X)) != ctgObj(err) || ctgIdent(cond.Y) == nil || ctgIdent(cond.Y).Name != "nil" {
			continue
		}
		if len(errCheck.Body.List) != 1 {
			continue
		}
		ret, ok := errCheck.Body.List[0].(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 || ctgObj(ctgIdent(ret.Results[0])) != ctgObj(err) {
			continue
		}
		foundCheck, ok := fl.Body.List[i+2].(*ast.IfStmt)
		if !ok || foundCheck.Init != nil || foundCheck.Else != nil || len(foundCheck.Body.List) != 1 {
			continue
		}
		absent, ok := foundCheck.Cond.(*ast.UnaryExpr)
		if !ok || absent.Op != token.NOT || ctgObj(ctgIdent(absent.X)) != ctgObj(found) {
			continue
		}
		ret, ok = foundCheck.Body.List[0].(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 || !ctgPackageIdent(f, ret.Results[0], "errTombstoneNoop") {
			continue
		}
		for _, later := range fl.Body.List[i+3:] {
			if ast.Node(later) == scrub {
				return ""
			}
		}
	}
	return "the erasure must follow lockTombstoneIn on the callback's own transaction and checks returning its error or errTombstoneNoop when absent"
}

func ctgPackageIdent(f *ctgFile, expr ast.Expr, name string) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == name && (ctgObj(id) == nil || ctgObj(id) == f.tree.Scope.Lookup(name))
}

// ctgIsAppReceiver reports whether x is the receiver of decl, a method on App
// or *App. It compares the parser's resolution, not the spelling, so a local
// such as a := directRunner{...} that reuses the receiver's name is another
// value.
func ctgIsAppReceiver(decl *ast.FuncDecl, x ast.Expr) bool {
	if decl.Recv == nil || len(decl.Recv.List) != 1 || len(decl.Recv.List[0].Names) != 1 {
		return false
	}
	fld := decl.Recv.List[0]
	t := guardkit.Unparen(fld.Type)
	if star, ok := t.(*ast.StarExpr); ok {
		t = guardkit.Unparen(star.X)
	}
	if id, ok := t.(*ast.Ident); !ok || id.Name != "App" {
		return false
	}
	recv, use := ctgObj(fld.Names[0]), ctgObj(ctgIdent(guardkit.Unparen(x)))
	return recv != nil && use == recv
}

func ctgFirstArg(call *ast.CallExpr) ast.Expr {
	if len(call.Args) == 0 {
		return nil
	}
	return call.Args[0]
}

// ctgViolations checks production sources keyed by slash path from the module
// root. Test files are skipped, as guardkit.ReadProduction skips them.
func ctgViolations(sources map[string]string) []string {
	run := &ctgRun{}
	production := guardkit.Sources{}
	for path, src := range sources {
		if !strings.HasSuffix(path, "_test.go") {
			production[path] = src
		}
	}
	parsed := production.Parse(0)
	run.bad = append(run.bad, parsed.Problems...)
	for _, pf := range parsed.Files {
		f := newCtgFile(pf.Path, parsed.Fset, pf.AST)
		run.imports(f)
		run.file(f)
	}
	if run.eraseCalls != 1 || run.eraseSites[ctgPipelineFile] != 1 {
		run.bad = append(run.bad, fmt.Sprintf("production tree: package level: R-6: eraseContactRequestTextIn is called %d times, want exactly one call, in eraseAccountIn (account_erasure.go), after the soft delete", run.eraseCalls))
	}
	if run.reasonCalls != 1 || run.reasonSites[ctgPipelineFile] != 1 {
		run.bad = append(run.bad, fmt.Sprintf("production tree: package level: R-6: eraseContactRequestReasonsIn is called %d times, want exactly one call, in eraseAccountIn (account_erasure.go), directly after eraseContactRequestTextIn", run.reasonCalls))
	}
	if run.pipelineCalls != 2 || run.pipelineSites["api_admin.go"] != 1 || run.pipelineSites["account_erasure_tombstones.go"] != 1 {
		run.bad = append(run.bad, fmt.Sprintf("production tree: package level: R-6: eraseAccountIn is called %d times, want exactly two calls, one in apiAdminDeleteUser (api_admin.go) in its a.fencedContactMutation callback and one in eraseTombstoneOnce (account_erasure_tombstones.go) in its a.contactMutationCtx callback", run.pipelineCalls))
	}
	sort.Strings(run.bad)
	return run.bad
}

func TestContactRequestTextChangesStayInTheirHelpers(t *testing.T) {
	sources := guardkit.Production(t)
	bad := ctgViolations(sources)
	for _, v := range bad {
		t.Error(v)
	}
	if len(bad) > 0 {
		t.Log(ctgLimits)
	}
}

// The clean fixture tree holds each allowed site once: the creation functions,
// the one ages-note UPDATE in its plain batch loop, the two callback calls,
// the testdb reset literal, and the statement_timeout set_config calls. Each
// bypass below changes it in one way.

const ctgAgesUpdate = `"UPDATE contact_requests SET add_childcare_ages_note = '' WHERE id IN ? AND purpose = 'occurrence_add' AND subject_user_id = ? AND add_childcare_ages_note <> ''"`

const ctgErasureLoop = "res := tx.Exec(" + ctgAgesUpdate + ", chunk, s.ID)\n\t\ttotal += res.RowsAffected"

const ctgEraseCall = "if err := eraseContactRequestTextIn(tx, s); err != nil {\n\t\t\treturn err\n\t\t}"

const ctgResetCall = `b.WriteString("SET LOCAL session_replication_role = replica;")`

// The reason helper's one UPDATE literal, its one call (directly after the
// ages-note call, in the admin callback) and its file (T07B-2b).
const ctgReasonUpdate = `"UPDATE contact_requests SET reason = '' WHERE id = ANY(?::bigint[]) AND reason <> '' AND (requester_id = ? OR subject_user_id = ?)"`

const ctgReasonLoop = "res := tx.Exec(" + ctgReasonUpdate + ", batch, s.ID, s.ID)\n\t\ttotal += res.RowsAffected"

const ctgReasonCall = "if err := eraseContactRequestReasonsIn(tx, s, plan); err != nil {\n\t\t\treturn err\n\t\t}"

func ctgReasonsFile(loop string) string {
	return `package main

import "gorm.io/gorm"

func eraseContactRequestReasonsIn(tx *gorm.DB, s erasureSubject, plan reasonPlan) error {
	var total int64
	for _, batch := range plan.batches {
		` + loop + `
	}
	_ = total
	return nil
}
`
}

func ctgErasureFile(loop string) string {
	return ctgErasureFileWith(loop, ctgPipeline("", ctgEraseCall))
}

// ctgErasureFileWith is the erasure file with the ages-note helper (its batch
// loop) and the given eraseAccountIn source.
func ctgErasureFileWith(loop, pipeline string) string {
	return `package main

import (
	"fold/internal/guardkit"
	"fold/models"
	"fmt"

	"gorm.io/gorm"
)

func eraseContactRequestTextIn(tx *gorm.DB, s erasureSubject) error {
	var ids []uint
	if err := tx.Raw("SELECT id FROM contact_requests WHERE purpose = 'occurrence_add' AND subject_user_id = ? AND add_childcare_ages_note <> '' ORDER BY id FOR UPDATE", s.ID).Scan(&ids).Error; err != nil {
		return err
	}
	var total int64
	for start := 0; start < len(ids); start += lookupChunk {
		chunk := ids[start:min(start+lookupChunk, len(ids))]
		` + loop + `
	}
	_ = fmt.Sprint(total)
	return nil
}
` + pipeline
}

// ctgPipeline is eraseAccountIn with before the soft delete and after it.
// Every fixture that calls the ages-note helper gets the reason helper's call
// directly after it, as production has, unless it brings its own.
func ctgPipeline(before, after string) string {
	if strings.Contains(after, "eraseContactRequestTextIn") && !strings.Contains(after, "eraseContactRequestReasonsIn") && !strings.Contains(after, "// no reasons call") {
		after += "\n\t" + ctgReasonCall
	}
	return `
func (a *App) eraseAccountIn(tx *gorm.DB, s erasureSubject, opts erasureOptions) (erasureCounts, error) {
	var out erasureCounts
	` + before + `
	if err := tx.Where("user_id = ?", s.ID).Delete(&models.Notification{}).Error; err != nil {
		return out, err
	}
	var plan reasonPlan
	if opts.Live != nil {
		if err := tx.Delete(opts.Live.User).Error; err != nil {
			return out, err
		}
		plan = opts.Live.Reasons
	}
	` + after + `
	return out, nil
}
`
}

// ctgPipelineCall is the one statement each erasure callback runs.
const ctgPipelineCall = "counts, err := a.eraseAccountIn(tx, s, opts)\n\t\t\tif err != nil {\n\t\t\t\treturn nil, err\n\t\t\t}"

func ctgAdminFile(before, after string) string {
	return ctgAdminFileShape("err = a.fencedContactMutation(ctx, contactChange{UserID: targetID}, ", ")", before, after)
}

// ctgAdminFileShape builds the admin fixture with the call's head (up to the
// callback), the text after the callback's closing brace, and the statements
// before and after the eraseAccountIn call.
func ctgAdminFileShape(head, tail, before, after string) string {
	return ctgAdminFileCall(head, tail, before+"\n\t\t\t"+ctgPipelineCall+"\n\t\t\t"+after)
}

// ctgAdminFileCall builds the admin fixture around the callback body.
func ctgAdminFileCall(head, tail, body string) string {
	return `package main

import (
	"fold/internal/guardkit"
	"fold/models"

	"gorm.io/gorm"
)

func (a *App) apiAdminDeleteUser(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), adminDeleteBudget)
	defer cancel()
	var err error
	for {
		` + head + `func(tx *gorm.DB) (*contactSeal, error) {
			` + body + `
			_ = counts
			return a.deletionSeal(me, nil), nil
		}` + tail + `
	}
	_ = err
}
`
}

const ctgTombstoneLock = `found, err := lockTombstoneIn(tx, id)
		if err != nil {
			return err
		}
		if !found {
			return errTombstoneNoop
		}`

// ctgTombstonePipelineCall is the sweep's eraseAccountIn statement.
const ctgTombstonePipelineCall = "counts, err := a.eraseAccountIn(tx, erasureSubject{ID: id}, erasureOptions{})\n\t\tif err != nil {\n\t\t\treturn err\n\t\t}\n\t\t_ = counts"

func ctgTombstoneFile(before, after string) string {
	return `package main

import "gorm.io/gorm"

func (a *App) eraseTombstoneOnce(ctx context.Context, id uint, version int) (tombstoneReport, error) {
	err := a.contactMutationCtx(ctx, contactChange{UserID: id}, func(tx *gorm.DB) error {
		` + before + `
		` + ctgTombstoneLock + `
		` + after + `
		return nil
	})
	return tombstoneReport{}, err
}
`
}

func ctgTestdbFile(extra string) string {
	return `package testdb

import (
	"fold/internal/guardkit"
	"strings"

	"gorm.io/gorm"
)

func resetStatements(db *gorm.DB) (string, string, error) {
	var b strings.Builder
	` + ctgResetCall + `
	for _, t := range tables {
		b.WriteString(" DELETE FROM " + t + ";")
	}
	return b.String(), "TRUNCATE TABLE " + strings.Join(tables, ", ") + " RESTART IDENTITY CASCADE", nil
}
` + extra
}

const ctgContactFile = `package main

import (
	"fold/internal/guardkit"
	"fold/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (a *App) apiCreateContactRequest(c *gin.Context) {
	err := a.contactMutation(contactChange{}, func(tx *gorm.DB) error {
		r := models.ContactRequest{Purpose: "contact", Reason: strings.TrimSpace(body.Reason)}
		var count int64
		if err = tx.Model(&models.ContactRequest{}).Where("requester_id = ? AND status = ?", 1, "pending").Count(&count).Error; err != nil {
			return err
		}
		return tx.Create(&r).Error
	})
	_ = err
}

func (a *App) apiCreateAddRequest(c *gin.Context) {
	err := a.contactMutation(contactChange{}, func(tx *gorm.DB) error {
		r := models.ContactRequest{Purpose: "group_add", Reason: strings.TrimSpace(body.Reason), AddOverLimit: body.OverLimit}
		r.AddChildcareCount, r.AddChildcareAgesNote = old.ChildcareCount, old.ChildcareAgesNote
		if body.ChildcareAgesNote != nil {
			r.AddChildcareAgesNote = strings.TrimSpace(*body.ChildcareAgesNote)
		}
		return tx.Create(&r).Error
	})
	_ = err
}

func (a *App) apiDecideContactRequest(c *gin.Context) {
	err := a.contactMutation(contactChange{}, func(tx *gorm.DB) error {
		var r models.ContactRequest
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&r, r.ID).Error; err != nil {
			return err
		}
		r.Status = "approved"
		r.EndReason = "decided"
		return tx.Save(&r).Error
	})
	_ = err
}

func presentAddRequest(r models.ContactRequest) addRequestJSON {
	out := addRequestJSON{Reason: r.Reason}
	out.Reason = ""
	return out
}

func finishRequests(tx *gorm.DB, r *models.ContactRequest, rs []models.ContactRequest) error {
	if err := tx.Save(rs).Error; err != nil {
		return err
	}
	return tx.Save(r).Error
}
`

const ctgTimeoutFile = `package main

import "gorm.io/gorm"

func withStatementTimeout(tx *gorm.DB) error {
	var prior string
	if err := tx.Raw("SELECT current_setting('statement_timeout')").Scan(&prior).Error; err != nil {
		return err
	}
	if err := tx.Exec("SELECT set_config('statement_timeout', ?, true)", "5000ms").Error; err != nil {
		return err
	}
	return tx.Exec("SELECT set_config('statement_timeout', ?, true)", prior).Error
}
`

const ctgLifecycleFile = `package main

import "gorm.io/gorm"

func (a *App) contactMutationCtx(tx *gorm.DB, remaining string) error {
	return tx.Exec("SELECT set_config('statement_timeout', ?, true), set_config('idle_in_transaction_session_timeout', ?, true)", "20000ms", remaining).Error
}
`

const ctgModelsFile = `package models

type ContactRequest struct {
	ID     uint
	Reason string
}

func All() []any { return []any{&ContactRequest{}} }
`

func ctgClean() map[string]string {
	return map[string]string{
		"account_erasure.go":            ctgErasureFile(ctgErasureLoop),
		"account_erasure_reasons.go":    ctgReasonsFile(ctgReasonLoop),
		"account_erasure_tombstones.go": ctgTombstoneFile("", ctgTombstonePipelineCall),
		"api_admin.go":                  ctgAdminFile("", ""),
		"api_contact.go":                ctgContactFile,
		"contact_lifecycle.go":          ctgLifecycleFile,
		"internal/testdb/testdb.go":     ctgTestdbFile(""),
		"statement_timeout.go":          ctgTimeoutFile,
		"models/models.go":              ctgModelsFile,
		"other.go":                      "package main\n\nfunc other() {}\n",
	}
}

// ctgBodyFile wraps statements in a function that has a loaded request r, a
// pointer p, a slice rs, a map m and a conflict clause oc in scope.
func ctgBodyFile(body string) string {
	return "package main\n\nimport (\n\t\"fold/models\"\n\n\t\"gorm.io/gorm\"\n\t\"gorm.io/gorm/clause\"\n)\n\n" +
		"func other(tx *gorm.DB, p *models.ContactRequest, rs []models.ContactRequest, m map[string]any, oc clause.OnConflict) {\n" +
		"\tvar r models.ContactRequest\n\t" + body + "\n}\n"
}

type ctgCase struct {
	name  string
	rule  string            // the rule that must reject it; empty means the tree must stay clean
	files map[string]string // replaces or adds files in the clean tree; "-" removes one
	want  string            // the file the violation must name (default other.go)
}

func ctgBodies(rule string, bodies ...string) []ctgCase {
	cases := make([]ctgCase, 0, len(bodies))
	for _, b := range bodies {
		cases = append(cases, ctgCase{name: b, rule: rule, files: map[string]string{"other.go": ctgBodyFile(b)}})
	}
	return cases
}

func ctgSource(name, rule, want string, files map[string]string) ctgCase {
	return ctgCase{name: name, rule: rule, want: want, files: files}
}

func ctgRunCase(t *testing.T, tc ctgCase, covered map[string]bool) {
	t.Helper()
	sources := ctgClean()
	for path, src := range tc.files {
		if src == "-" {
			delete(sources, path)
		} else {
			sources[path] = src
		}
	}
	bad := ctgViolations(sources)
	for _, v := range bad {
		if strings.Contains(v, "does not parse") {
			t.Errorf("%s: the fixture does not parse: %s", tc.name, v)
			return
		}
	}
	if tc.rule == "" {
		if len(bad) != 0 {
			t.Errorf("%s: want no violation, got %v", tc.name, bad)
		}
		return
	}
	want := tc.want
	if want == "" {
		want = "other.go"
	}
	for _, v := range bad {
		if strings.HasPrefix(v, want) && strings.Contains(v, ": "+tc.rule+": ") {
			covered[tc.rule] = true
			return
		}
	}
	t.Errorf("%s: want a %s violation in %s, got %v", tc.name, tc.rule, want, bad)
}

func TestContactRequestTextGuardCatchesBypasses(t *testing.T) {
	if bad := ctgViolations(ctgClean()); len(bad) != 0 {
		t.Fatalf("the clean fixture failed: %v", bad)
	}
	if !strings.Contains(ctgLimits, "'DELETE FROM ' + t") || !strings.Contains(ctgLimits, "not a type checker") {
		t.Fatal("the limits text must say that computed-table-name SQL such as internal/testdb's 'DELETE FROM ' + t is not seen, and that the guard is not a type checker")
	}
	contactWith := func(old, replacement string) string {
		if !strings.Contains(ctgContactFile, old) {
			t.Fatalf("fixture anchor %q is missing from the clean api_contact.go", old)
		}
		return strings.Replace(ctgContactFile, old, replacement, 1)
	}
	testdbWith := func(old, replacement string) string {
		clean := ctgTestdbFile("")
		if !strings.Contains(clean, old) {
			t.Fatalf("fixture anchor %q is missing from the clean testdb.go", old)
		}
		return strings.Replace(clean, old, replacement, 1)
	}
	testdbOnly := func(source string) map[string]string {
		return map[string]string{"internal/testdb/testdb.go": source}
	}
	var cases []ctgCase

	// R-1: field writes on a bound ContactRequest value.
	cases = append(cases, ctgBodies("R-1",
		`r.Reason = ""`,
		`r.AddChildcareAgesNote = ""`,
		`rs[0].Reason = ""`,
		`p.Reason = ""`,
		`(*p).Reason = ""`,
		`r.Reason += "x"`,
		`r.Reason, r.Status = "", "x"`,
		`q := &rs[0]; q.Reason = ""`,
		`v := &r; v.AddChildcareAgesNote = ""`,
		`for _, x := range rs { x.AddChildcareAgesNote = "" }`,
		// T07A-GR-01: parentheses around the assigned selector change nothing.
		`(r.Reason) = ""`,
		`(r.AddChildcareAgesNote) = ""`,
		`((r.Reason)) = ""`,
		`(r).Reason = ""`,
		`(rs[0].Reason) = ""`,
		`(p.Reason) = ""`,
		`((*p).AddChildcareAgesNote) = ""`,
		`(r.Reason) += "x"`,
		`(r.Reason), r.Status = "", "x"`,
		`if err := tx.First(&r, 1).Error; err != nil { return }; (r.Reason) = ""; tx.Save(&r)`,
		`if err := tx.First(&r, 1).Error; err != nil { return }; (r.AddChildcareAgesNote) = ""; tx.Save(&r)`,
		// T07A-GR-03: a closure that captures the request is the same request, and a
		// block-scoped request is still a request.
		`func() { r.Reason = "" }()`,
		`{ r := models.ContactRequest{}; r.Reason = "" }`,
		`func(r models.ContactRequest) { r.Reason = "" }(r)`,
	)...)
	cases = append(cases,
		ctgSource("Reason assigned inside a creation function", "R-1", "api_contact.go", map[string]string{
			"api_contact.go": contactWith("r.AddChildcareCount, r.AddChildcareAgesNote = old.ChildcareCount, old.ChildcareAgesNote",
				"r.AddChildcareCount, r.AddChildcareAgesNote = old.ChildcareCount, old.ChildcareAgesNote\n\t\tr.Reason = \"x\""),
		}),
		ctgSource("ages note assigned in the other creation function", "R-1", "api_contact.go", map[string]string{
			"api_contact.go": contactWith("var count int64", "r.AddChildcareAgesNote = \"x\"\n\t\tvar count int64"),
		}),
		ctgSource("a package models method body", "R-1", "models/other.go", map[string]string{
			"models/other.go": "package models\n\nfunc f() {\n\tvar r ContactRequest\n\tr.Reason = \"\"\n}\n",
		}),
		ctgSource("aliased models import", "R-1", "", map[string]string{
			"other.go": "package main\n\nimport (\n\tmm \"fold/models\"\n)\n\nfunc other() {\n\tvar r mm.ContactRequest\n\tr.Reason = \"\"\n}\n",
		}),
		ctgSource("dot-imported models", "R-1", "", map[string]string{
			"other.go": "package main\n\nimport . \"fold/models\"\n\nfunc other() {\n\tvar r ContactRequest\n\tr.Reason = \"\"\n}\n",
		}),
	)

	// R-2: literals that set a protected field, and literal overwrites.
	cases = append(cases, ctgBodies("R-2",
		`_ = models.ContactRequest{Reason: "x"}`,
		`_ = &models.ContactRequest{AddChildcareAgesNote: "x"}`,
		`rs = []models.ContactRequest{{Reason: "x"}}`,
		`_ = []*models.ContactRequest{{Reason: "x"}}`,
		`_ = map[string]models.ContactRequest{"a": {Reason: "x"}}`,
		`_ = models.ContactRequest{1, 2}`,
		`*p = models.ContactRequest{ID: r.ID}`,
		`rs[0] = models.ContactRequest{ID: 1}`,
		`r = models.ContactRequest{ID: r.ID}`,
		`tx.Save(&models.ContactRequest{ID: 1})`,
	)...)
	cases = append(cases,
		ctgSource("a creation function name in another file", "R-2", "api_other.go", map[string]string{
			"api_other.go": "package main\n\nimport \"fold/models\"\n\nfunc apiCreateAddRequest() { _ = models.ContactRequest{Reason: \"x\"} }\n",
		}),
		ctgSource("a third function in api_contact.go", "R-2", "api_contact.go", map[string]string{
			"api_contact.go": ctgContactFile + "\nfunc (a *App) apiCreateGroupAsk() { _ = models.ContactRequest{Reason: \"x\"} }\n",
		}),
		ctgSource("ages note literal in the contact creator", "R-2", "api_contact.go", map[string]string{
			"api_contact.go": contactWith(`Purpose: "contact",`, `Purpose: "contact", AddChildcareAgesNote: "x",`),
		}),
	)

	// R-3: GORM update chains on a contact_requests target (kinds a to d).
	cases = append(cases, ctgBodies("R-3",
		`tx.Model(&models.ContactRequest{}).Where("id = ?", 1).Update("reason", "")`,
		`tx.Table("contact_requests").Where("id = ?", 1).UpdateColumn("reason", "")`,
		`tx.Table("public.contact_requests").Update("reason", "")`,
		`tx.Table("contact_requests AS c").Update("reason", "")`,
		`tx.Table("\"public\".\"contact_requests\"").Update("reason", "")`,
		`tx.Model(&r).Updates(map[string]any{"reason": ""})`,
		`tx.Model(&r).UpdateColumns(map[string]any{"add_childcare_ages_note": ""})`,
		`tx.Model(&r).Updates(map[string]any{"Reason": ""})`,
		`tx.Model(&r).Updates(models.ContactRequest{Reason: "x"})`,
		`tx.Model(&r).Updates(&models.ContactRequest{AddChildcareAgesNote: "x"})`,
		`tx.Model(&r).Updates(m)`,
		`tx.Model(&r).Updates(r)`,
		`tx.Where("id = ?", 1).Updates(&r)`,
		`tx.Where("id = ?", 1).UpdateColumns(r)`,
		`tx.Where("id = ?", 1).Updates(&models.ContactRequest{Reason: ""})`,
		`q := tx.Model(&r).Where("id = ?", 1); q.Update("reason", "")`,
		`tx.Unscoped().Model(&r).Update("Reason", "")`,
		`tx.Model(&r).Update("contact_requests.reason", "")`,
		`col := "reason"; tx.Model(&r).Update(col, "")`,
		// T07A-GR-03: a same-named q declared in a closure is another variable, so
		// the outer q keeps its contact_requests prefix; a closure that captures q
		// still sees it; a shadowing q that holds the request chain is read too.
		`q := tx.Model(&r).Where("id = ?", id); func() { q := tx.Model(&models.Setting{}); _ = q }(); q.Update("reason", "")`,
		`q := tx.Model(&r).Where("id = ?", id); { q := tx.Model(&models.Setting{}); _ = q }; q.Update("reason", "")`,
		`q := tx.Model(&r); func() { q.Update("reason", "") }()`,
		`q := tx.Model(&models.Setting{}); _ = q; func() { q := tx.Model(&r); q.Update("reason", "") }()`,
		`var q *gorm.DB; q = tx.Model(&r); q.Update("reason", "")`,
	)...)
	cases = append(cases,
		ctgSource("aliased models import", "R-3", "", map[string]string{
			"other.go": "package main\n\nimport (\n\tmm \"fold/models\"\n)\n\nfunc other() {\n\tvar r mm.ContactRequest\n\ttx.Model(&r).Updates(map[string]any{\"reason\": \"\"})\n}\n",
		}),
		ctgSource("dot-imported models", "R-3", "", map[string]string{
			"other.go": "package main\n\nimport . \"fold/models\"\n\nfunc other() {\n\tvar r ContactRequest\n\ttx.Model(&r).Update(\"reason\", \"\")\n}\n",
		}),
	)

	// R-4a: conflict clauses, including UpdateAll.
	cases = append(cases, ctgBodies("R-4a",
		`tx.Clauses(clause.OnConflict{DoUpdates: clause.AssignmentColumns([]string{"reason"})}).Create(&r)`,
		`tx.Clauses(clause.OnConflict{DoUpdates: clause.AssignmentColumns([]string{"add_childcare_ages_note"})}).Create(&r)`,
		`tx.Clauses(clause.OnConflict{DoUpdates: clause.Assignments(map[string]any{"reason": ""})}).CreateInBatches(&rs, 100)`,
		`tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&r)`,
		`tx.Clauses(clause.OnConflict{UpdateAll: true}).Save(&r)`,
		`tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&models.ContactRequest{ID: 1})`,
		`tx.Clauses(oc).Create(&r)`,
		`tx.Clauses(clause.OnConflict{DoUpdates: someSet}).Create(&r)`,
		// T07A-GR-03: the conflict clause sits in a once-assigned q whose name is also
		// declared in a closure.
		`q := tx.Clauses(clause.OnConflict{UpdateAll: true}); func() { q := tx.Model(&models.Setting{}); _ = q }(); q.Create(&r)`,
	)...)
	cases = append(cases,
		ctgSource("UpdateAll inside both creation functions", "R-4a", "api_contact.go", map[string]string{
			"api_contact.go": strings.ReplaceAll(ctgContactFile, "return tx.Create(&r).Error", "return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&r).Error"),
		}),
		ctgSource("conflict clause through a cl alias", "R-4a", "", map[string]string{
			"other.go": "package main\n\nimport (\n\t\"fold/models\"\n\tcl \"gorm.io/gorm/clause\"\n)\n\nfunc other(tx *gorm.DB) {\n\tvar r models.ContactRequest\n\ttx.Clauses(cl.OnConflict{UpdateAll: true}).Create(&r)\n}\n",
		}),
	)

	// R-4b: find-or-create forms.
	cases = append(cases, ctgBodies("R-4b",
		`tx.Where("id = ?", 1).Assign(map[string]any{"reason": ""}).FirstOrCreate(&r)`,
		`tx.Where("id = ?", 1).Assign(models.ContactRequest{Reason: ""}).FirstOrCreate(&models.ContactRequest{})`,
		`tx.Where(map[string]any{"reason": "x"}).FirstOrCreate(&r)`,
		`tx.FirstOrCreate(&r, map[string]any{"reason": "x"})`,
		`tx.Attrs(map[string]any{"add_childcare_ages_note": ""}).FirstOrInit(&r)`,
		`tx.Assign(m).FirstOrCreate(&r)`,
		`tx.Where("reason = ?", "x").FirstOrCreate(&r)`,
		`tx.Where("id = ?", 1).Attrs(models.ContactRequest{Reason: "x"}).FirstOrCreate(&r)`,
		`tx.Where(m).FirstOrCreate(&r)`,
		`q := tx.Where("reason = ?", "x"); func() { q := tx.Model(&models.Setting{}); _ = q }(); q.FirstOrCreate(&r)`,
	)...)

	// R-4c: Select before a write.
	cases = append(cases, ctgBodies("R-4c",
		`tx.Model(&r).Select("reason").Updates(map[string]any{"name": ""})`,
		`tx.Select("reason").Save(&r)`,
		`tx.Select("*").Save(&r)`,
		`tx.Select(cols).Save(&r)`,
		`tx.Select([]string{"reason"}).Save(&r)`,
		`q := tx.Select("reason"); func() { q := tx.Model(&models.Setting{}); _ = q }(); q.Save(&r)`,
	)...)

	// R-5: raw SQL writes.
	cases = append(cases, ctgBodies("R-5",
		`tx.Exec("UPDATE contact_requests SET reason = ''")`,
		"tx.Exec(`UPDATE public.\"contact_requests\" AS c SET \"reason\" = ''`)",
		`tx.Exec("UPDATE contact_requests SET add_childcare_ages_note = '', reason = ''")`,
		`tx.Exec("UPDATE contact_requests SET status = 'x' WHERE reason <> ''")`,
		`tx.Exec("UPDATE ONLY contact_requests SET reason = ''")`,
		`tx.Exec("INSERT INTO contact_requests (id) VALUES (1) ON CONFLICT (id) DO UPDATE SET reason = ''")`,
		`tx.Exec("MERGE INTO contact_requests c USING x ON c.id = x.id WHEN MATCHED THEN UPDATE SET reason = ''")`,
		`tx.Exec("UPDATE contact_requests " + "SET reason = ''")`,
		`const q = "UPDATE contact_requests SET "; tx.Exec(q + "reason = ''")`,
		`tx.Exec(`+ctgAgesUpdate+`, 1, 2)`,
		// T07A-GR-02: a semicolon inside a quote, comment or dollar quote does not end
		// the statement, so the SET list is read to its real end.
		`tx.Exec("UPDATE contact_requests SET status = status || left(';', 0), reason = '' WHERE purpose = 'contact'")`,
		`tx.Exec("UPDATE contact_requests SET status = 'a;b', add_childcare_ages_note = '' WHERE id = 1")`,
		`tx.Exec("UPDATE contact_requests SET status = 'it''s;', reason = ''")`,
		`tx.Exec("UPDATE contact_requests SET status = E'\\';', reason = ''")`,
		"tx.Exec(`UPDATE contact_requests SET status = status || left(E'\\\"' || ';', 0), reason = '' WHERE purpose = 'contact'`)",
		"tx.Exec(`UPDATE contact_requests SET status = status || left(E'\\\"' || ';', 0), add_childcare_ages_note = '' WHERE id = 1`)",
		`tx.Exec("UPDATE contact_requests SET status = $$;$$, reason = ''")`,
		`tx.Exec("UPDATE contact_requests SET status = $q$ ; $q$, reason = ''")`,
		`tx.Exec("UPDATE contact_requests SET status = status -- ;\n, reason = ''")`,
		`tx.Exec("UPDATE contact_requests SET status = status /* ; */, reason = ''")`,
		`tx.Exec("UPDATE contact_requests SET status = 'open, reason = ''")`,
		`tx.Exec("INSERT INTO contact_requests (id) VALUES (1) ON CONFLICT (id) DO UPDATE SET status = ';', reason = ''")`,
		`tx.Exec("UPDATE contact_requests SET status = 'x'; UPDATE contact_requests SET reason = ''")`,
		// T07A-GFR-01: a semicolon inside a double-quoted identifier does not end the
		// statement either, so a SET list after the alias is still read.
		"tx.Exec(`UPDATE contact_requests AS \"r;kept\" SET reason = '' WHERE purpose = 'contact'`)",
		"tx.Exec(`UPDATE contact_requests AS \"r;kept\" SET add_childcare_ages_note = '' WHERE id = 1`)",
		"tx.Exec(`UPDATE \"contact_requests\" AS \"r;kept\" SET \"reason\" = ''`)",
		"tx.Exec(`UPDATE contact_requests SET \"st;atus\" = 'x', reason = ''`)",
		"tx.Exec(`UPDATE contact_requests AS \"r\"\";kept\" SET reason = ''`)",
		"tx.Exec(`UPDATE contact_requests AS \"r;kept SET reason = ''`)",
		"tx.Exec(`INSERT INTO contact_requests (id) VALUES (1) ON CONFLICT (id) DO UPDATE SET \"a;b\" = 1, reason = ''`)",
		// T07A-GFR-02: a constant that a closure or block redeclares is its own
		// constant; the use after the closure still reads the outer one.
		`const q = "UPDATE contact_requests SET "; func() { const q = "UPDATE settings SET "; _ = q }(); tx.Exec(q + "reason = ''")`,
		`const q = "UPDATE settings SET "; func() { const q = "UPDATE contact_requests SET "; tx.Exec(q + "reason = ''") }()`,
		`const q = "UPDATE contact_requests SET "; { const q = "UPDATE settings SET "; _ = q }; tx.Exec(q + "reason = ''")`,
		`const a = "UPDATE contact_requests "; const q = a + "SET "; func() { const a = "UPDATE settings "; _ = a }(); tx.Exec(q + "reason = ''")`,
	)...)
	cases = append(cases,
		ctgSource("a package constant used by a function", "R-5", "other.go", map[string]string{
			"other.go": "package main\n\nconst q = \"UPDATE contact_requests SET \"\n\nfunc other(tx *gorm.DB) {\n\ttx.Exec(q + \"reason = ''\")\n}\n",
		}),
		ctgSource("a package constant declared after its use", "R-5", "other.go", map[string]string{
			"other.go": "package main\n\nfunc other(tx *gorm.DB) {\n\ttx.Exec(q + \"reason = ''\")\n}\n\nconst q = \"UPDATE contact_requests SET \"\n",
		}),
		ctgSource("a second literal in eraseContactRequestTextIn that sets reason", "R-5", "account_erasure.go", map[string]string{
			"account_erasure.go": ctgErasureFile(ctgErasureLoop + "\n\t\ttx.Exec(\"UPDATE contact_requests SET reason = '' WHERE id = 1\")"),
		}),
		ctgSource("the ages literal in a func literal", "R-5", "account_erasure.go", map[string]string{
			"account_erasure.go": ctgErasureFile("func() { tx.Exec(" + ctgAgesUpdate + ", chunk, s.ID) }()"),
		}),
		ctgSource("the ages literal twice", "R-5", "account_erasure.go", map[string]string{
			"account_erasure.go": ctgErasureFile(ctgErasureLoop + "\n\t\ttx.Exec(" + ctgAgesUpdate + ", chunk, s.ID)"),
		}),
		ctgSource("the ages literal with reason in its SET list", "R-5", "account_erasure.go", map[string]string{
			"account_erasure.go": ctgErasureFile("res := tx.Exec(\"UPDATE contact_requests SET add_childcare_ages_note = '', reason = '' WHERE id IN ?\", chunk)\n\t\ttotal += res.RowsAffected"),
		}),
		ctgSource("the ages literal in another function of account_erasure.go", "R-5", "account_erasure.go", map[string]string{
			"account_erasure.go": ctgErasureFile(ctgErasureLoop) + "\nfunc eraseElsewhere(tx *gorm.DB) { tx.Exec(" + ctgAgesUpdate + ", 1, 2) }\n",
		}),
		ctgSource("the ages literal in a copy of the helper in another file", "R-5", "api_other.go", map[string]string{
			"account_erasure.go": ctgErasureFile("total++"),
			"api_other.go":       "package main\n\nfunc eraseContactRequestTextInCopy(tx *gorm.DB) { tx.Exec(" + ctgAgesUpdate + ", 1, 2) }\n",
		}),
	)

	// T07B-2b: the reason column is blanked by one literal in
	// eraseContactRequestReasonsIn and nowhere else.
	scrubs := func(after string) string { return ctgErasureFileWith(ctgErasureLoop, ctgPipeline("", after)) }
	cases = append(cases,
		ctgSource("a second reason literal in the helper", "R-5", "account_erasure_reasons.go", map[string]string{
			"account_erasure_reasons.go": ctgReasonsFile(ctgReasonLoop + "\n\t\ttx.Exec(" + ctgReasonUpdate + ", batch, s.ID, s.ID)"),
		}),
		ctgSource("the reason literal in a func literal", "R-5", "account_erasure_reasons.go", map[string]string{
			"account_erasure_reasons.go": ctgReasonsFile("func() { tx.Exec(" + ctgReasonUpdate + ", batch, s.ID, s.ID) }()"),
		}),
		ctgSource("the reason literal in another function of the file", "R-5", "account_erasure_reasons.go", map[string]string{
			"account_erasure_reasons.go": ctgReasonsFile(ctgReasonLoop) + "\nfunc blankElsewhere(tx *gorm.DB) { tx.Exec(" + ctgReasonUpdate + ", 1, 2, 3) }\n",
		}),
		ctgSource("the reason literal in another file", "R-5", "api_other.go", map[string]string{
			"api_other.go": "package main\n\nfunc blank(tx *gorm.DB) { tx.Exec(" + ctgReasonUpdate + ", 1, 2, 3) }\n",
		}),
		ctgSource("the reason literal with the ages note in its SET list", "R-5", "account_erasure_reasons.go", map[string]string{
			"account_erasure_reasons.go": ctgReasonsFile("res := tx.Exec(\"UPDATE contact_requests SET reason = '', add_childcare_ages_note = '' WHERE id = ANY(?::bigint[])\", batch)\n\t\ttotal += res.RowsAffected"),
		}),
		ctgSource("the reason literal setting another column too", "R-5", "account_erasure_reasons.go", map[string]string{
			"account_erasure_reasons.go": ctgReasonsFile("res := tx.Exec(\"UPDATE contact_requests SET reason = '', status = 'x' WHERE id = ANY(?::bigint[])\", batch)\n\t\ttotal += res.RowsAffected"),
		}),
		// The reason helper's one call: in eraseAccountIn, directly after the
		// ages-note scrub.
		ctgSource("the reason helper's call twice", "R-6", "production tree", map[string]string{
			"account_erasure.go": scrubs(ctgEraseCall + "\n\t" + ctgReasonCall + "\n\t" + ctgReasonCall),
		}),
		ctgSource("no call of the reason helper", "R-6", "production tree", map[string]string{
			"account_erasure.go": scrubs(ctgEraseCall + "\n\t// no reasons call"),
		}),
		ctgSource("the reason call before the ages-note call", "R-6", "account_erasure.go", map[string]string{
			"account_erasure.go": scrubs(ctgReasonCall + "\n\t" + ctgEraseCall),
		}),
		ctgSource("a statement between the two calls", "R-6", "account_erasure.go", map[string]string{
			"account_erasure.go": scrubs(ctgEraseCall + "\n\t_ = plan\n\t" + ctgReasonCall),
		}),
		ctgSource("the reason call before the soft delete", "R-6", "account_erasure.go", map[string]string{
			"account_erasure.go": ctgErasureFileWith(ctgErasureLoop, ctgPipeline(ctgReasonCall, ctgEraseCall+"\n\t// no reasons call")),
		}),
		ctgSource("the reason call passing a.DB", "R-6", "account_erasure.go", map[string]string{
			"account_erasure.go": scrubs(ctgEraseCall + "\n\tif err := eraseContactRequestReasonsIn(a.DB, s, plan); err != nil {\n\t\treturn out, err\n\t}"),
		}),
		ctgSource("the reason call nested in a block", "R-6", "account_erasure.go", map[string]string{
			"account_erasure.go": scrubs(ctgEraseCall + "\n\tif true {\n\t\t_ = eraseContactRequestReasonsIn(tx, s, plan)\n\t}"),
		}),
		ctgSource("the reason call in a go statement", "R-6", "account_erasure.go", map[string]string{
			"account_erasure.go": scrubs(ctgEraseCall + "\n\tgo eraseContactRequestReasonsIn(tx, s, plan)"),
		}),
		ctgSource("the reason call from another function", "R-6", "account_erasure.go", map[string]string{
			"account_erasure.go": scrubs(ctgEraseCall) + "\nfunc (a *App) otherErasure(tx *gorm.DB, s erasureSubject, plan reasonPlan) error {\n\treturn eraseContactRequestReasonsIn(tx, s, plan)\n}\n",
		}),
		ctgSource("the reason call from the tombstone callback", "R-6", "account_erasure_tombstones.go", map[string]string{
			"account_erasure_tombstones.go": ctgTombstoneFile("", ctgTombstonePipelineCall+"\n\t\t"+ctgReasonCall),
		}),
		ctgSource("the reason call from the admin callback", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": ctgAdminFile("", ctgReasonCall),
		}),
		ctgSource("the reason helper declared as a method", "R-5", "account_erasure_reasons.go", map[string]string{
			"account_erasure_reasons.go": strings.Replace(ctgReasonsFile(ctgReasonLoop), "func eraseContactRequestReasonsIn(", "func (a *App) eraseContactRequestReasonsIn(", 1),
		}),
		ctgSource("the reason call on a copy of the pipeline's tx", "R-6", "account_erasure.go", map[string]string{
			"account_erasure.go": scrubs(ctgEraseCall + "\n\ttx2 := tx\n\tif err := eraseContactRequestReasonsIn(tx2, s, plan); err != nil {\n\t\treturn out, err\n\t}"),
		}),
		ctgSource("the reason call inside a closure of the pipeline", "R-6", "account_erasure.go", map[string]string{
			"account_erasure.go": scrubs(ctgEraseCall + "\n\tfunc() { _ = eraseContactRequestReasonsIn(tx, s, plan) }()"),
		}),
		ctgSource("the reason call in a defer statement", "R-6", "account_erasure.go", map[string]string{
			"account_erasure.go": scrubs(ctgEraseCall + "\n\tdefer eraseContactRequestReasonsIn(tx, s, plan)"),
		}),
		ctgSource("the reason helper used as a value", "R-6", "other.go", map[string]string{
			"other.go": "package main\n\nfunc other() {\n\tf := eraseContactRequestReasonsIn\n\t_ = f\n}\n",
		}),
	)

	// R-6: the ages-note scrub's one call, in eraseAccountIn after the soft
	// delete, on its own transaction.
	cases = append(cases,
		ctgSource("a second call", "R-6", "production tree", map[string]string{"account_erasure.go": scrubs(ctgEraseCall + "\n\t" + ctgEraseCall)}),
		ctgSource("no call at all", "R-6", "production tree", map[string]string{"account_erasure.go": scrubs("// no reasons call")}),
		ctgSource("a call before the soft delete", "R-6", "account_erasure.go", map[string]string{
			"account_erasure.go": ctgErasureFileWith(ctgErasureLoop, ctgPipeline(ctgEraseCall, "")),
		}),
		ctgSource("a call before a soft delete of another value", "R-6", "account_erasure.go", map[string]string{
			"account_erasure.go": strings.Replace(scrubs(ctgEraseCall), "tx.Delete(opts.Live.User)", "tx.Delete(opts.Live.Other)", 1),
		}),
		ctgSource("a soft delete of a local, not the options", "R-6", "account_erasure.go", map[string]string{
			"account_erasure.go": strings.Replace(scrubs(ctgEraseCall), "if opts.Live != nil {\n\t\tif err := tx.Delete(opts.Live.User)", "if opts := other; opts.Live != nil {\n\t\tif err := tx.Delete(opts.Live.User)", 1),
		}),
		ctgSource("a call passing a.DB", "R-6", "account_erasure.go", map[string]string{"account_erasure.go": scrubs("if err := eraseContactRequestTextIn(a.DB, s); err != nil {\n\t\treturn out, err\n\t}")}),
		ctgSource("a call in a tx.Transaction body", "R-6", "account_erasure.go", map[string]string{
			"account_erasure.go": scrubs("if err := tx.Transaction(func(tx2 *gorm.DB) error {\n\t\treturn eraseContactRequestTextIn(tx2, s)\n\t}); err != nil {\n\t\treturn out, err\n\t}"),
		}),
		ctgSource("a go statement", "R-6", "account_erasure.go", map[string]string{"account_erasure.go": scrubs("go eraseContactRequestTextIn(tx, s)")}),
		ctgSource("a defer statement", "R-6", "account_erasure.go", map[string]string{"account_erasure.go": scrubs("defer eraseContactRequestTextIn(tx, s)")}),
		ctgSource("a call nested in a block", "R-6", "account_erasure.go", map[string]string{"account_erasure.go": scrubs("if true {\n\t\t_ = eraseContactRequestTextIn(tx, s)\n\t}")}),
		ctgSource("a returned call", "R-6", "account_erasure.go", map[string]string{"account_erasure.go": scrubs("return out, eraseContactRequestTextIn(tx, s)")}),
		ctgSource("a call from another function of account_erasure.go", "R-6", "account_erasure.go", map[string]string{
			"account_erasure.go": strings.Replace(scrubs(ctgEraseCall), "func (a *App) eraseAccountIn(", "func (a *App) eraseOther(", 1),
		}),
		ctgSource("the pipeline in another file", "R-6", "account_erasure_extra.go", map[string]string{
			"account_erasure.go":       ctgErasureFileWith(ctgErasureLoop, ""),
			"account_erasure_extra.go": "package main\n\nimport \"gorm.io/gorm\"\n" + ctgPipeline("", ctgEraseCall),
		}),
		ctgSource("eraseAccountIn as a function, not an App method", "R-6", "account_erasure.go", map[string]string{
			"account_erasure.go": strings.Replace(scrubs(ctgEraseCall), "func (a *App) eraseAccountIn(", "func eraseAccountIn(", 1),
		}),
		ctgSource("eraseAccountIn on another type", "R-6", "account_erasure.go", map[string]string{
			"account_erasure.go": strings.Replace(scrubs(ctgEraseCall), "func (a *App) eraseAccountIn(", "func (a *Other) eraseAccountIn(", 1),
		}),
		ctgSource("eraseAccountIn's transaction not its first parameter", "R-6", "account_erasure.go", map[string]string{
			"account_erasure.go": strings.Replace(scrubs(ctgEraseCall), "eraseAccountIn(tx *gorm.DB, s erasureSubject,", "eraseAccountIn(s erasureSubject, tx *gorm.DB,", 1),
		}),
		ctgSource("a call from any other function", "R-6", "other.go", map[string]string{
			"other.go": "package main\n\nfunc other(tx *gorm.DB, s erasureSubject) error {\n\treturn eraseContactRequestTextIn(tx, s)\n}\n",
		}),
		ctgSource("a function value", "R-6", "other.go", map[string]string{
			"other.go": "package main\n\nfunc other() {\n\tf := eraseContactRequestTextIn\n\t_ = f\n}\n",
		}),
		ctgSource("a method value", "R-6", "other.go", map[string]string{
			"other.go": "package main\n\nfunc (a *App) other() {\n\tf := a.eraseContactRequestTextIn\n\t_ = f\n}\n",
		}),
		ctgSource("an argument", "R-6", "other.go", map[string]string{
			"other.go": "package main\n\nfunc other() {\n\trun(eraseContactRequestTextIn)\n}\n",
		}),
		ctgSource("a returned function", "R-6", "other.go", map[string]string{
			"other.go": "package main\n\nfunc other() any {\n\treturn eraseContactRequestTextIn\n}\n",
		}),
		// T07A-GFR-03: the first argument is the pipeline's own parameter, not a
		// local that reuses its name, and the same holds for the soft delete the
		// call must follow.
		ctgSource("an if-init local that shadows the pipeline tx", "R-6", "account_erasure.go", map[string]string{
			"account_erasure.go": scrubs("if tx := a.DB; eraseContactRequestTextIn(tx, s) != nil {\n\t\treturn out, gorm.ErrInvalidDB\n\t}"),
		}),
		ctgSource("a pipeline parameter named _", "R-6", "account_erasure.go", map[string]string{
			"account_erasure.go": strings.Replace(scrubs(ctgEraseCall), "eraseAccountIn(tx *gorm.DB,", "eraseAccountIn(_ *gorm.DB,", 1),
		}),
		ctgSource("a soft delete on a local that shadows the pipeline tx", "R-6", "account_erasure.go", map[string]string{
			"account_erasure.go": strings.Replace(scrubs(ctgEraseCall), "if err := tx.Delete(opts.Live.User).Error; err != nil {", "if tx := a.DB; tx.Delete(opts.Live.User).Error != nil {", 1),
		}),
	)

	// R-6: eraseAccountIn's two calls, each directly in its erasure callback on
	// the callback's own transaction and the method's own App receiver.
	admin := ctgAdminFile("", "")
	pipelineCall := "counts, err := a.eraseAccountIn(tx, s, opts)"
	cases = append(cases,
		ctgSource("a second admin call", "R-6", "production tree", map[string]string{"api_admin.go": ctgAdminFile("", ctgPipelineCall)}),
		ctgSource("no admin call", "R-6", "production tree", map[string]string{"api_admin.go": ctgAdminFileCall("err = a.fencedContactMutation(ctx, contactChange{UserID: targetID}, ", ")", "counts := 0")}),
		ctgSource("the admin call under a.contactMutationCtx", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": ctgAdminFileShape("err = a.contactMutationCtx(ctx, contactChange{UserID: targetID}, ", ")", "", ""),
		}),
		ctgSource("the old a.contactMutation at the admin site", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": ctgAdminFileShape("err = a.contactMutation(contactChange{UserID: targetID}, ", ")", "", ""),
		}),
		ctgSource("a callback that is not the last argument", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": ctgAdminFileShape("err = a.fencedContactMutation(ctx, contactChange{UserID: targetID}, ", ", nil)", "", ""),
		}),
		ctgSource("the admin call passing a.DB", "R-6", "api_admin.go", map[string]string{"api_admin.go": strings.Replace(admin, pipelineCall, "counts, err := a.eraseAccountIn(a.DB, s, opts)", 1)}),
		ctgSource("the admin call on a copy of the callback's tx", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": strings.Replace(admin, pipelineCall, "tx2 := tx\n\t\t\tcounts, err := a.eraseAccountIn(tx2, s, opts)", 1),
		}),
		ctgSource("the admin call nested in a block", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": ctgAdminFileCall("err = a.fencedContactMutation(ctx, contactChange{UserID: targetID}, ", ")", "counts := 0\n\t\t\tif true {\n\t\t\t\t_, _ = a.eraseAccountIn(tx, s, opts)\n\t\t\t}"),
		}),
		ctgSource("the admin call in a closure", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": ctgAdminFileCall("err = a.fencedContactMutation(ctx, contactChange{UserID: targetID}, ", ")", "counts := 0\n\t\t\tfunc() { _, _ = a.eraseAccountIn(tx, s, opts) }()"),
		}),
		ctgSource("the admin call in a go statement", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": ctgAdminFileCall("err = a.fencedContactMutation(ctx, contactChange{UserID: targetID}, ", ")", "counts := 0\n\t\t\tgo a.eraseAccountIn(tx, s, opts)"),
		}),
		ctgSource("the admin call in a defer statement", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": ctgAdminFileCall("err = a.fencedContactMutation(ctx, contactChange{UserID: targetID}, ", ")", "counts := 0\n\t\t\tdefer a.eraseAccountIn(tx, s, opts)"),
		}),
		ctgSource("the wrapper call inside a func literal", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": strings.Replace(strings.Replace(admin, "\t\terr = a.fencedContactMutation(", "\t\tfunc() {\n\t\terr = a.fencedContactMutation(", 1), "\t\t})\n\t}\n\t_ = err", "\t\t})\n\t\t}()\n\t}\n\t_ = err", 1),
		}),
		ctgSource("a call from another function of api_admin.go", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": strings.Replace(admin, "apiAdminDeleteUser", "apiAdminOtherThing", 1),
		}),
		ctgSource("the admin call in another file", "R-6", "api_admin_extra.go", map[string]string{"api_admin.go": "-", "api_admin_extra.go": admin}),
		ctgSource("a call from any other method", "R-6", "other.go", map[string]string{
			"other.go": "package main\n\nfunc (a *App) other(tx *gorm.DB) {\n\t_, _ = a.eraseAccountIn(tx, erasureSubject{}, erasureOptions{})\n}\n",
		}),
		ctgSource("eraseAccountIn as a method value", "R-6", "other.go", map[string]string{
			"other.go": "package main\n\nfunc (a *App) other() {\n\tf := a.eraseAccountIn\n\t_ = f\n}\n",
		}),
		ctgSource("eraseAccountIn passed as an argument", "R-6", "other.go", map[string]string{
			"other.go": "package main\n\nfunc (a *App) other() {\n\trun(a.eraseAccountIn)\n}\n",
		}),
		ctgSource("eraseAccountIn on another receiver", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": strings.Replace(admin, pipelineCall, "counts, err := b.eraseAccountIn(tx, s, opts)", 1),
		}),
		// T07A-GR-04: a method named like the wrapper on another receiver is not
		// the transaction-and-gate wrapper, whether it is a different variable or
		// a local that reuses the receiver's name.
		ctgSource("the wrapper on another receiver", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": strings.Replace(admin, "\t\terr = a.fencedContactMutation(", "\t\trunner := directRunner{db: a.DB}\n\t\terr = runner.fencedContactMutation(", 1),
			"api_runner.go": "package main\n\nimport \"gorm.io/gorm\"\n\ntype directRunner struct{ db *gorm.DB }\n\n" +
				"func (d directRunner) fencedContactMutation(_ context.Context, _ contactChange, fn func(*gorm.DB) (*contactSeal, error)) error { _, err := fn(d.db); return err }\n",
		}),
		ctgSource("the wrapper on a local that shadows the receiver", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": strings.Replace(admin, "\t\terr = a.fencedContactMutation(", "\t\ta := directRunner{db: a.DB}\n\t\terr = a.fencedContactMutation(", 1),
			"api_runner.go": "package main\n\nimport \"gorm.io/gorm\"\n\ntype directRunner struct{ db *gorm.DB }\n\n" +
				"func (d directRunner) fencedContactMutation(_ context.Context, _ contactChange, fn func(*gorm.DB) (*contactSeal, error)) error { _, err := fn(d.db); return err }\n",
		}),
		ctgSource("the wrapper on a parameter, not the receiver", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": strings.Replace(strings.Replace(admin, "(a *App) apiAdminDeleteUser(c *gin.Context)", "(a *App) apiAdminDeleteUser(c *gin.Context, runner directRunner)", 1),
				"err = a.fencedContactMutation(", "err = runner.fencedContactMutation(", 1),
		}),
		ctgSource("apiAdminDeleteUser as a function, not an App method", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": strings.Replace(admin, "func (a *App) apiAdminDeleteUser(", "func apiAdminDeleteUser(a *App, ", 1),
		}),
		ctgSource("apiAdminDeleteUser on another type", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": strings.Replace(admin, "(a *App) apiAdminDeleteUser", "(a *Other) apiAdminDeleteUser", 1),
		}),
		ctgSource("an if-init local that shadows the callback tx", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": ctgAdminFileCall("err = a.fencedContactMutation(ctx, contactChange{UserID: targetID}, ", ")", "counts := 0\n\t\t\tif tx := a.DB; true {\n\t\t\t\t_, _ = a.eraseAccountIn(tx, s, opts)\n\t\t\t}"),
		}),
		ctgSource("a callback parameter named _ and an unresolved tx argument", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": strings.Replace(admin, "func(tx *gorm.DB)", "func(_ *gorm.DB)", 1),
		}),
	)
	tombstone := ctgTombstoneFile("", ctgTombstonePipelineCall)
	tombstoneCall := "a.eraseAccountIn(tx, erasureSubject{ID: id}, erasureOptions{})"
	tombstoneBad := []struct{ name, source string }{
		{"a tombstone call before its lock", ctgTombstoneFile(ctgTombstonePipelineCall, "")},
		{"a tombstone call in another function", strings.Replace(tombstone, "eraseTombstoneOnce(", "other(", 1)},
		{"a tombstone call in runSweepPageSize", strings.Replace(tombstone, "eraseTombstoneOnce(", "runSweepPageSize(", 1)},
		{"a tombstone call in a nested transaction", ctgTombstoneFile("", "return tx.Transaction(func(tx2 *gorm.DB) error { _, err := a.eraseAccountIn(tx2, erasureSubject{ID: id}, erasureOptions{}); return err })")},
		{"a tombstone call passing a.DB", strings.Replace(tombstone, tombstoneCall, "a.eraseAccountIn(a.DB, erasureSubject{ID: id}, erasureOptions{})", 1)},
		{"a tombstone method on another type", strings.Replace(tombstone, "(a *App)", "(a *Other)", 1)},
		{"a tombstone callback not last", strings.Replace(tombstone, "\n\t})", "\n\t}, nil)", 1)},
		{"a tombstone call on another receiver", strings.Replace(tombstone, "a.contactMutationCtx", "runner.contactMutationCtx", 1)},
		{"a tombstone through the fenced wrapper", strings.Replace(tombstone, "a.contactMutationCtx", "a.fencedContactMutation", 1)},
		{"a tombstone pipeline on another receiver", strings.Replace(tombstone, tombstoneCall, "b.eraseAccountIn(tx, erasureSubject{ID: id}, erasureOptions{})", 1)},
		{"a tombstone call in a job handler", ctgTombstoneFile("", "return func(ctx context.Context) error { _, err := "+tombstoneCall+"; return err }(ctx)")},
		{"a tombstone call nested in a block", ctgTombstoneFile("", "if true { _, _ = "+tombstoneCall+" }")},
		{"a tombstone go call", ctgTombstoneFile("", "go "+tombstoneCall)},
		{"a tombstone deferred call", ctgTombstoneFile("", "defer "+tombstoneCall)},
		{"a tombstone call with shadowed tx", ctgTombstoneFile("", "if tx := a.DB; true { _, _ = a.eraseAccountIn(tx, erasureSubject{ID: id}, erasureOptions{}) }")},
		{"a tombstone lock on a.DB", strings.Replace(tombstone, "lockTombstoneIn(tx, id)", "lockTombstoneIn(a.DB, id)", 1)},
		{"a tombstone lock without error check", strings.Replace(tombstone, "if err != nil {\n\t\t\treturn err\n\t\t}\n\t\tif !found", "if !found", 1)},
		{"a tombstone lock without found check", strings.Replace(tombstone, "if !found {\n\t\t\treturn errTombstoneNoop\n\t\t}", "", 1)},
		{"a tombstone lock with reversed found check", strings.Replace(tombstone, "if !found", "if found", 1)},
		{"a tombstone lock with a swallowed error", strings.Replace(tombstone, "return err\n", "return nil\n", 1)},
		{"a shadowed tombstone lock helper", ctgTombstoneFile("lockTombstoneIn := otherLock", ctgTombstonePipelineCall)},
		{"a shadowed tombstone no-op sentinel", ctgTombstoneFile("errTombstoneNoop := otherError", ctgTombstonePipelineCall)},
	}
	for _, tc := range tombstoneBad {
		cases = append(cases, ctgSource(tc.name, "R-6", "account_erasure_tombstones.go", map[string]string{
			"account_erasure_tombstones.go": tc.source,
		}))
	}
	cases = append(cases,
		ctgSource("a second tombstone callback call", "R-6", "production tree", map[string]string{
			"account_erasure_tombstones.go": ctgTombstoneFile("", ctgTombstonePipelineCall+"\n"+strings.Replace(ctgTombstonePipelineCall, ":=", "=", 1)),
		}),
		ctgSource("no tombstone call", "R-6", "production tree", map[string]string{
			"account_erasure_tombstones.go": ctgTombstoneFile("", ""),
		}),
		ctgSource("two admin calls cannot replace the tombstone call", "R-6", "production tree", map[string]string{
			"account_erasure_tombstones.go": ctgTombstoneFile("", ""),
			"api_admin.go":                  ctgAdminFile("", ctgPipelineCall),
		}),
		ctgSource("a third site in the tombstone file", "R-6", "account_erasure_tombstones.go", map[string]string{
			"account_erasure_tombstones.go": tombstone + "\nfunc (a *App) other(tx *gorm.DB) error { _, err := " + tombstoneCall + "; return err }\n",
		}),
		ctgSource("an ages-note call left in the tombstone callback", "R-6", "account_erasure_tombstones.go", map[string]string{
			"account_erasure_tombstones.go": ctgTombstoneFile("", ctgTombstonePipelineCall+"\n\t\t"+ctgEraseCall),
		}),
		ctgSource("an ages-note call left in the admin callback", "R-6", "api_admin.go", map[string]string{
			"api_admin.go": ctgAdminFile("", ctgEraseCall),
		}),
	)

	// R-7: row deletion.
	cases = append(cases, ctgBodies("R-7",
		`tx.Delete(&r)`,
		`tx.Delete(&rs, 1)`,
		`tx.Unscoped().Delete(p)`,
		`tx.Where("id = ?", 1).Delete(&models.ContactRequest{})`,
		`tx.Table("contact_requests").Where("id = ?", 1).Delete(nil)`,
		`tx.Model(&r).Where("id = ?", 1).Delete(nil)`,
		// T07A-GR-03: the prefix survives a same-named q declared in a closure.
		`q := tx.Model(&r).Where("id = ?", 1); func() { q := tx.Model(&models.Setting{}); _ = q }(); q.Delete(nil)`,
		`q := tx.Table("contact_requests"); func() { q := tx.Table("settings"); _ = q }(); q.Delete(nil)`,
		`tx.Exec("DELETE FROM contact_requests WHERE id = 1")`,
		`tx.Exec("DELETE FROM ONLY public.contact_requests")`,
		`tx.Exec("TRUNCATE TABLE users, contact_requests")`,
	)...)
	cases = append(cases,
		ctgSource("aliased models import", "R-7", "", map[string]string{
			"other.go": "package main\n\nimport mm \"fold/models\"\n\nfunc other() {\n\ttx.Delete(&mm.ContactRequest{})\n}\n",
		}),
	)

	// RC-1 (b), (c) and (e): trigger and replication-role bypasses in Go literals.
	cases = append(cases, ctgBodies("RC-1(b)",
		`tx.Exec("ALTER TABLE contact_requests DISABLE TRIGGER add_request_identity")`,
		`tx.Exec("ALTER TABLE contact_requests ENABLE REPLICA TRIGGER add_request_identity")`,
		`tx.Exec("ALTER TABLE contact_requests ENABLE ALWAYS TRIGGER add_request_completed")`,
		`tx.Exec("DROP TRIGGER add_request_identity ON contact_requests")`,
		`tx.Exec("CREATE OR REPLACE FUNCTION add_request_identity() RETURNS trigger AS $$ BEGIN RETURN NEW; END $$ LANGUAGE plpgsql")`,
		`tx.Exec("CREATE FUNCTION public.add_request_completed() RETURNS trigger AS $$ BEGIN RETURN NEW; END $$ LANGUAGE plpgsql")`,
		`tx.Exec("SET LOCAL session_replication_role = replica")`,
		`tx.Exec("SET LOCAL session_replication_role = replica;")`,
		`tx.Exec("SELECT set_config(?, ?, true)", "session_replication_role", "replica")`,
		`tx.Exec("SELECT set_config('session_replication_role', 'replica', true)")`,
		`tx.Exec("SET LOCAL session_" + "replication_role = replica")`,
	)...)
	cases = append(cases,
		ctgSource("the reset literal in a resetStatements of another file", "RC-1(b)", "internal/testdb/reset.go", map[string]string{
			"internal/testdb/reset.go": "package testdb\n\nimport \"strings\"\n\nfunc resetStatements() {\n\tvar b strings.Builder\n\t" + ctgResetCall + "\n}\n",
		}),
		ctgSource("the reset literal in another function of testdb.go", "RC-1(b)", "internal/testdb/testdb.go", testdbOnly(
			ctgTestdbFile("\nfunc other(b *strings.Builder) {\n\t"+ctgResetCall+"\n}\n"))),
		ctgSource("a second reset literal in the real shape", "RC-1(b)", "internal/testdb/testdb.go", testdbOnly(
			testdbWith(ctgResetCall, ctgResetCall+"\n\t"+ctgResetCall))),
		ctgSource("a changed reset literal", "RC-1(b)", "internal/testdb/testdb.go", testdbOnly(
			testdbWith(ctgResetCall, `b.WriteString("SET session_replication_role = replica;")`))),
		ctgSource("the reset literal sent through tx.Exec", "RC-1(b)", "internal/testdb/testdb.go", testdbOnly(
			testdbWith(ctgResetCall, `db.Exec("SET LOCAL session_replication_role = replica;")`))),
		ctgSource("the reset literal on another builder", "RC-1(b)", "internal/testdb/testdb.go", testdbOnly(
			testdbWith(ctgResetCall, `sb.WriteString("SET LOCAL session_replication_role = replica;")`))),
		ctgSource("the reset literal in a func literal", "RC-1(b)", "internal/testdb/testdb.go", testdbOnly(
			testdbWith(ctgResetCall, "func() {\n\t\t"+ctgResetCall+"\n\t}()"))),
		ctgSource("the reset literal in a method of the same name", "RC-1(b)", "internal/testdb/testdb.go", testdbOnly(
			testdbWith("func resetStatements(", "func (x thing) resetStatements("))),
	)

	// RC-1 (d): production code never imports internal/testdb.
	for name, imp := range map[string]string{
		"plain": `import "fold/internal/testdb"`, "aliased": `import td "fold/internal/testdb"`,
		"blank": `import _ "fold/internal/testdb"`, "dot": `import . "fold/internal/testdb"`,
	} {
		cases = append(cases,
			ctgSource("a "+name+" import of internal/testdb", "RC-1(d)", "api_x.go", map[string]string{"api_x.go": "package main\n\n" + imp + "\n"}),
			ctgSource("a "+name+" import of internal/testdb from a deep package", "RC-1(d)", "internal/other/x.go", map[string]string{"internal/other/x.go": "package other\n\n" + imp + "\n"}),
		)
	}

	// Allowed: the forms the design lists as negative fixtures, each of which
	// must leave the clean tree clean.
	allowed := []string{
		`var out struct{ Reason string }; out.Reason = ""`,
		`r.EndReason = "x"`,
		`tx.Exec("UPDATE messages SET reason = CASE WHEN x THEN '' ELSE reason END")`,
		`meta := map[string]any{"reason": "x"}; _ = meta`,
		`var n int64; tx.Model(&models.ContactRequest{}).Where("requester_id = ?", 1).Count(&n)`,
		`tx.Model(&models.ContactRequest{}).Where("id = ?", 1).Find(&rs)`,
		`tx.Model(&models.ContactRequest{}).Where("id = ?", 1).First(&r)`,
		`var x []string; tx.Model(&models.ContactRequest{}).Pluck("reason", &x)`,
		`tx.Save(&r)`,
		`tx.Save(p)`,
		`tx.Save(&rs)`,
		`tx.Save(rs)`,
		`tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&r)`,
		`tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&models.ContactRequest{ID: 1})`,
		`tx.Clauses(clause.OnConflict{DoUpdates: clause.AssignmentColumns([]string{"status"})}).Create(&r)`,
		`tx.Clauses(clause.Locking{Strength: "UPDATE"}).Save(&r)`,
		`tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&r, 1)`,
		`tx.Where(models.Setting{Key: "k"}).Assign(map[string]any{"value": "v"}).FirstOrCreate(&models.Setting{})`,
		`tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&models.Setting{})`,
		`s := models.Setting{}; tx.Clauses(clause.OnConflict{UpdateAll: true, DoUpdates: someSet}).Create(&s)`,
		`tx.Model(&r).Update("status", "x")`,
		`tx.Model(&r).Updates(map[string]any{"status": "x"})`,
		`tx.Omit("reason").Save(&r)`,
		`tx.Select("status").Save(&r)`,
		`tx.Where("id = ?", 1).FirstOrCreate(&r)`,
		`tx.FirstOrCreate(&r, "id = ?", 1)`,
		`tx.Where("end_reason = ?", 1).FirstOrCreate(&r)`,
		`tx.Model(&models.Registration{}).Where("id = ?", 1).Update("manage_token_hash", "")`,
		`tx.Where("id = ?", 1).Delete(&models.Notification{})`,
		`tx.Exec("DELETE FROM guest_signups WHERE email = ?", "x")`,
		`tx.Exec("SELECT set_config('statement_timeout', ?, true)", "5000ms")`,
		`tx.Raw("SELECT current_setting('statement_timeout')").Scan(&x)`,
		// T07A-GR-01: parentheses around an unprotected or non-request selector.
		`var out struct{ Reason string }; (out.Reason) = ""`,
		`(r.EndReason) = "x"`,
		// T07A-GR-02: the statement still ends at its real semicolon.
		`tx.Exec("UPDATE contact_requests SET status = 'x' WHERE id = 1; UPDATE messages SET reason = ''")`,
		`tx.Exec("UPDATE contact_requests SET status = ';' WHERE id = 1; UPDATE messages SET reason = ''")`,
		`tx.Exec("UPDATE contact_requests SET status = $$;$$ WHERE id = 1; UPDATE messages SET reason = ''")`,
		// T07A-GFR-01: the statement still ends at its real semicolon after a quoted
		// alias, and a double quote inside a single-quoted string opens no identifier.
		"tx.Exec(`UPDATE contact_requests AS \"r;kept\" SET status = 'x' WHERE id = 1; UPDATE messages SET reason = ''`)",
		"tx.Exec(`UPDATE contact_requests SET status = 'say \"hi' WHERE id = 1; UPDATE messages SET reason = ''`)",
		"tx.Exec(`UPDATE contact_requests SET status = status || left(E'\\\"' || ';', 0) WHERE id = 1; UPDATE messages SET reason = ''`)",
		"tx.Exec(`UPDATE messages AS \"r;kept\" SET reason = ''`)",
		// T07A-GFR-02: a constant that a closure, block or local variable redeclares
		// is not the outer constant, in either direction.
		`const q = "UPDATE settings SET "; func() { const q = "UPDATE contact_requests SET "; _ = q }(); tx.Exec(q + "reason = ''")`,
		`const q = "UPDATE settings SET "; { const q = "UPDATE contact_requests SET "; _ = q }; tx.Exec(q + "reason = ''")`,
		`const a = "UPDATE settings "; const q = a + "SET "; func() { const a = "UPDATE contact_requests "; _ = a }(); tx.Exec(q + "reason = ''")`,
		`const q = "UPDATE contact_requests SET "; func() { q := "UPDATE settings SET "; tx.Exec(q + "reason = ''") }()`,
		// T07A-GR-03: a DTO or a Setting chain that reuses a request's name in an
		// inner scope is not the request, in either direction.
		`func(r struct{ Reason string }) { r.Reason = "" }(struct{ Reason string }{})`,
		`_ = r; func(r struct{ Reason string }) { r.Reason = "" }(struct{ Reason string }{})`,
		`{ r := struct{ Reason string }{}; r.Reason = "" }`,
		`func(p *struct{ Reason string }) { p.Reason = "" }(nil)`,
		`q := tx.Model(&r); func() { q := tx.Model(&models.Setting{}); q.Update("reason", "") }(); _ = q`,
		`q := tx.Model(&models.Setting{}); func() { q := tx.Model(&r); _ = q }(); q.Update("reason", "")`,
		`q := tx.Model(&models.Setting{}); func() { q := tx.Model(&r); _ = q }(); q.Delete(nil)`,
	}
	for _, b := range allowed {
		cases = append(cases, ctgCase{name: b, files: map[string]string{"other.go": ctgBodyFile(b)}})
	}
	cases = append(cases,
		ctgCase{name: "tombstone package declarations resolve in their own file", files: map[string]string{
			"account_erasure_tombstones.go": tombstone + "\nfunc lockTombstoneIn(tx *gorm.DB, id uint) (bool, error) { return true, nil }\nvar errTombstoneNoop = errors.New(\"no work\")\n",
		}},
		ctgCase{name: "tombstone renamed callback parameter", files: map[string]string{
			"account_erasure_tombstones.go": strings.NewReplacer("func(tx *gorm.DB)", "func(tombTx *gorm.DB)", "lockTombstoneIn(tx,", "lockTombstoneIn(tombTx,", "a.eraseAccountIn(tx,", "a.eraseAccountIn(tombTx,").Replace(tombstone),
		}},
		ctgCase{name: "tombstone renamed lock results", files: map[string]string{
			"account_erasure_tombstones.go": strings.NewReplacer("found", "locked", "err :=", "lockErr :=", "err !=", "lockErr !=", "return err\n", "return lockErr\n", "{}, err", "{}, lockErr").Replace(tombstone),
		}},
		ctgCase{name: "an assignment call in the pipeline body", files: map[string]string{
			"account_erasure.go": scrubs("err := eraseContactRequestTextIn(tx, s)\n\tif err != nil {\n\t\treturn out, err\n\t}"),
		}},
		ctgCase{name: "an expression statement call in the pipeline body", files: map[string]string{
			"account_erasure.go": scrubs("eraseContactRequestTextIn(tx, s)"),
		}},
		// T07A-GFR-03: a call in an if condition that uses the pipeline's own tx.
		ctgCase{name: "a condition call using the pipeline parameter", files: map[string]string{
			"account_erasure.go": scrubs("if eraseContactRequestTextIn(tx, s) != nil {\n\t\treturn out, gorm.ErrInvalidDB\n\t}"),
		}},
		ctgCase{name: "a condition call with an init that does not shadow tx", files: map[string]string{
			"account_erasure.go": scrubs("if n := 1; eraseContactRequestTextIn(tx, s) != nil && n > 0 {\n\t\treturn out, gorm.ErrInvalidDB\n\t}"),
		}},
		ctgCase{name: "the pipeline's transaction renamed", files: map[string]string{
			"account_erasure.go": strings.NewReplacer("eraseAccountIn(tx *gorm.DB", "eraseAccountIn(etx *gorm.DB", "tx.Where(", "etx.Where(", "tx.Delete(opts", "etx.Delete(opts", "eraseContactRequestTextIn(tx,", "eraseContactRequestTextIn(etx,", "eraseContactRequestReasonsIn(tx,", "eraseContactRequestReasonsIn(etx,").Replace(scrubs(ctgEraseCall)),
		}},
		ctgCase{name: "an if-init admin call", files: map[string]string{
			"api_admin.go": ctgAdminFileCall("err = a.fencedContactMutation(ctx, contactChange{UserID: targetID}, ", ")", "counts := 0\n\t\t\tif _, err := a.eraseAccountIn(tx, s, opts); err != nil {\n\t\t\t\treturn nil, err\n\t\t\t}"),
		}},
		ctgCase{name: "a ContactRequest type of another package", files: map[string]string{
			"other.go": "package main\n\nimport \"fold/internal/policy\"\n\nfunc other() {\n\tvar r policy.ContactRequest\n\tr.Reason = \"\"\n}\n",
		}},
		ctgCase{name: "a bare ContactRequest in package policy", files: map[string]string{
			"internal/policy/contact.go": "package policy\n\ntype ContactRequest struct{ Reason string }\n\nfunc f() {\n\tvar r ContactRequest\n\tr.Reason = \"\"\n}\n",
		}},
		ctgCase{name: "a test file that sets the role and imports internal/testdb", files: map[string]string{
			"api_x_test.go": "package main\n\nimport \"fold/internal/testdb\"\n\nvar q = \"SET LOCAL session_replication_role = replica; ALTER TABLE x DISABLE TRIGGER y\"\n",
		}},
		ctgCase{name: "a package inside internal/testdb", files: map[string]string{
			"internal/testdb/helper.go": "package testdb\n\nfunc helper() {}\n",
		}},
		ctgCase{name: "the models package declaring the type", files: map[string]string{"models/models.go": ctgModelsFile}},
	)

	covered := map[string]bool{}
	for _, tc := range cases {
		ctgRunCase(t, tc, covered)
	}
	// Mutation M11: deleting any one rule must fail the matching positive fixture.
	for _, rule := range []string{"R-1", "R-2", "R-3", "R-4a", "R-4b", "R-4c", "R-5", "R-6", "R-7", "RC-1(b)", "RC-1(d)"} {
		if !covered[rule] {
			t.Errorf("no positive fixture proves that %s rejects anything", rule)
		}
	}
}

// The scan must reach the files the real allowances and the import check are
// about. A tree that stops holding them would let the guard pass on nothing.
func TestContactRequestTextGuardReadsTheRealTree(t *testing.T) {
	sources := guardkit.Production(t)
	for _, path := range []string{"account_erasure.go", "account_erasure_tombstones.go", "api_admin.go", "api_contact.go", "internal/testdb/testdb.go"} {
		if _, ok := sources[path]; !ok {
			t.Errorf("guardkit.ReadProduction no longer reads %s, which the contact-request text guard binds its allowances to", path)
		}
	}
	for path := range sources {
		if strings.HasSuffix(path, "_test.go") {
			t.Errorf("guardkit.ReadProduction returned the test file %s", path)
		}
	}
}
