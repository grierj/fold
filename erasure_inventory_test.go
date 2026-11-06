package main

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"fold/internal/testdb"
	"fold/models"

	"gorm.io/gorm"
)

type erasureInventoryField struct {
	Table  string
	Column string
}

// Load base tables separately from their columns with a left join, so even
// a new zero-column table needs an inventory heading and a model.
func erasureCatalog(t *testing.T, db *gorm.DB) []erasureInventoryField {
	t.Helper()
	var fields []erasureInventoryField
	err := db.Raw(`SELECT t.table_name AS "table", COALESCE(c.column_name, '') AS "column"
		FROM information_schema.tables t
		LEFT JOIN information_schema.columns c
		  ON c.table_schema = t.table_schema AND c.table_name = t.table_name
		WHERE t.table_schema = 'public' AND t.table_type = 'BASE TABLE'
		  AND t.table_name <> 'goose_db_version'
		ORDER BY t.table_name, c.ordinal_position`).Scan(&fields).Error
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) == 0 {
		t.Fatal("erasure inventory: empty application catalog")
	}
	return fields
}

func erasureModelFields(t *testing.T, db *gorm.DB) []erasureInventoryField {
	t.Helper()
	var fields []erasureInventoryField
	tables := make(map[string]bool)
	for _, model := range models.All() {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(model); err != nil {
			t.Fatalf("erasure inventory: parse %T: %v", model, err)
		}
		table := stmt.Schema.Table
		if tables[table] {
			t.Fatalf("erasure inventory: duplicate model table %s", table)
		}
		tables[table] = true
		fields = append(fields, erasureInventoryField{Table: table})
		for _, column := range stmt.Schema.DBNames {
			fields = append(fields, erasureInventoryField{Table: table, Column: column})
		}
	}
	return fields
}

// This checks coverage and references only. It does not turn a Proposed or
// gated disposition into permission to retain data, or test runtime erasure.
func erasureInventoryProblems(columns []erasureColumn, catalog, modelFields []erasureInventoryField, doc string) []string {
	rules := make(map[string]bool)
	tableRows := make(map[string]string)
	heading := regexp.MustCompile(`^#{1,6} ([0-9]+(?:\.[0-9]+)*|T[0-9]+)(?:[. ]|$)`)
	tableHeading := regexp.MustCompile("^### (T[0-9]+) `([a-z0-9_]+)`")
	rowID := regexp.MustCompile("^\\| `?([A-Z]+-?[0-9]+[A-Za-z0-9-]*)`? \\|")
	for _, line := range strings.Split(doc, "\n") {
		if m := heading.FindStringSubmatch(line); m != nil {
			rules[m[1]] = true
		}
		if m := tableHeading.FindStringSubmatch(line); m != nil {
			tableRows[m[2]] = m[1]
		}
		if m := rowID.FindStringSubmatch(line); m != nil {
			rules[m[1]] = true
		}
	}

	index := func(fields []erasureInventoryField) map[string]map[string]bool {
		out := make(map[string]map[string]bool)
		for _, f := range fields {
			if out[f.Table] == nil {
				out[f.Table] = make(map[string]bool)
			}
			if f.Column != "" {
				out[f.Table][f.Column] = true
			}
		}
		return out
	}
	actual, modeled := index(catalog), index(modelFields)
	classified := make(map[string]map[string]bool)
	var bad []string
	for _, c := range columns {
		name := c.Table + "." + c.Column
		if classified[c.Table] == nil {
			classified[c.Table] = make(map[string]bool)
		}
		if classified[c.Table][c.Column] {
			bad = append(bad, "duplicate classification: "+name)
		}
		classified[c.Table][c.Column] = true
		if c.Column == "" || !actual[c.Table][c.Column] {
			bad = append(bad, "stale classification: "+name)
		}
		if tableRows[c.Table] == "" || tableRows[c.Table] != c.Row {
			bad = append(bad, fmt.Sprintf("%s: row %q does not match its T-row heading", name, c.Row))
		}
		switch c.Kind {
		case "DEL", "ANON", "ATTR", "NA":
		case "COND", "RET":
			if len(c.Rules) == 0 {
				bad = append(bad, name+": "+c.Kind+" requires a rule")
			}
		default:
			bad = append(bad, name+": unknown kind "+c.Kind)
		}
		for _, rule := range c.Rules {
			if !rules[rule] {
				bad = append(bad, fmt.Sprintf("%s: unknown rule %q", name, rule))
			}
		}
	}
	for table, fields := range actual {
		if classified[table] == nil {
			bad = append(bad, "unclassified table: "+table)
		}
		if tableRows[table] == "" {
			bad = append(bad, "table without T-row heading: "+table)
		}
		if modeled[table] == nil {
			bad = append(bad, "catalog table missing from models.All(): "+table)
		}
		for column := range fields {
			if !classified[table][column] {
				bad = append(bad, "unclassified column: "+table+"."+column)
			}
		}
	}
	for table, fields := range modeled {
		if actual[table] == nil {
			bad = append(bad, "model table missing from catalog: "+table)
		}
		if classified[table] == nil {
			bad = append(bad, "unclassified model table: "+table)
		}
		for column := range fields {
			if !classified[table][column] {
				bad = append(bad, "unclassified model column: "+table+"."+column)
			}
			if !actual[table][column] {
				bad = append(bad, "model column missing from catalog: "+table+"."+column)
			}
		}
	}
	sort.Strings(bad)
	return bad
}

func TestErasureInventoryCoversTheSchema(t *testing.T) {
	doc, err := os.ReadFile("docs/plan/M7.4-ERASURE-INVENTORY.md")
	if err != nil {
		t.Fatal(err)
	}
	db := testdb.Shared(t)
	modelFields := erasureModelFields(t, db)
	if bad := erasureInventoryProblems(erasureColumns, erasureCatalog(t, db), modelFields, string(doc)); len(bad) != 0 {
		t.Fatalf("erasure coverage:\n%s", strings.Join(bad, "\n"))
	}

	t.Run("scratch ALTER TABLE is rejected", func(t *testing.T) {
		// PostgreSQL DDL is transactional. Leave the shared migrated schema
		// unchanged even if the assertion fails.
		tx := db.Begin()
		if tx.Error != nil {
			t.Fatal(tx.Error)
		}
		defer tx.Rollback()
		if err := tx.Exec("ALTER TABLE users ADD COLUMN erasure_inventory_scratch text").Error; err != nil {
			t.Fatal(err)
		}
		bad := erasureInventoryProblems(erasureColumns, erasureCatalog(t, tx), modelFields, string(doc))
		want := "unclassified column: users.erasure_inventory_scratch"
		if !slices.Contains(bad, want) {
			t.Fatalf("scratch ALTER TABLE was not detected: %v", bad)
		}
		t.Logf("scratch ALTER TABLE detected: %s", want)
	})
}

func TestErasureInventoryRejectsCoverageGaps(t *testing.T) {
	const doc = "## 4. Tables\n### T01 `sample`\n### 6.2 Keys\n| M23 | Copy |\n| O-4 | Evidence |\n"
	base := []erasureColumn{{"sample", "note", "T01", "COND", []string{"6.2", "M23", "O-4"}}}
	fields := []erasureInventoryField{{"sample", "note"}}
	if bad := erasureInventoryProblems(base, fields, fields, doc); len(bad) != 0 {
		t.Fatalf("valid fixture rejected: %v", bad)
	}
	for _, tc := range []struct {
		name string
		edit func(*erasureColumn)
		want string
	}{
		{"rule-less COND", func(c *erasureColumn) { c.Rules = nil }, "sample.note: COND requires a rule"},
		{"rule-less RET", func(c *erasureColumn) { c.Kind, c.Rules = "RET", nil }, "sample.note: RET requires a rule"},
		{"unknown rule", func(c *erasureColumn) { c.Rules = []string{"missing"} }, `sample.note: unknown rule "missing"`},
		{"rule mentioned only in prose", func(c *erasureColumn) { c.Rules = []string{"M99"} }, `sample.note: unknown rule "M99"`},
		{"wrong row", func(c *erasureColumn) { c.Row = "T02" }, `sample.note: row "T02" does not match its T-row heading`},
		{"unknown kind", func(c *erasureColumn) { c.Kind = "KEEP" }, "sample.note: unknown kind KEEP"},
		{"stale column", func(c *erasureColumn) { c.Column = "old_note" }, "stale classification: sample.old_note"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			columns := slices.Clone(base)
			tc.edit(&columns[0])
			bad := erasureInventoryProblems(columns, fields, fields, doc+"M99 is mentioned here, not defined.\n")
			if !slices.Contains(bad, tc.want) {
				t.Fatalf("want %q, got %v", tc.want, bad)
			}
			t.Logf("rejected: %s", tc.want)
		})
	}
	for _, tc := range []struct {
		name    string
		columns []erasureColumn
		catalog []erasureInventoryField
		models  []erasureInventoryField
		doc     string
		want    string
	}{
		{"unclassified column", nil, fields, fields, doc, "unclassified column: sample.note"},
		{"unclassified table", nil, fields, fields, doc, "unclassified table: sample"},
		{"missing heading", base, fields, fields, "## 4. Tables\n", "table without T-row heading: sample"},
		{"stale table", base, nil, nil, doc, "stale classification: sample.note"},
		{"duplicate column", append(slices.Clone(base), base[0]), fields, fields, doc, "duplicate classification: sample.note"},
		{"unclassified model", nil, nil, fields, doc, "unclassified model table: sample"},
		{"unclassified model column", base, fields, append(slices.Clone(fields), erasureInventoryField{"sample", "extra"}), doc, "unclassified model column: sample.extra"},
		{"catalog table without model", base, fields, nil, doc, "catalog table missing from models.All(): sample"},
		{"model table without catalog", base, nil, fields, doc, "model table missing from catalog: sample"},
		{"zero-column table", base, append(slices.Clone(fields), erasureInventoryField{Table: "empty"}), fields, doc, "unclassified table: empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := erasureInventoryProblems(tc.columns, tc.catalog, tc.models, tc.doc)
			if !slices.Contains(bad, tc.want) {
				t.Fatalf("want %q, got %v", tc.want, bad)
			}
		})
	}
}
