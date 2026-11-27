// Package migrations holds the schema, one forward-only file per step
// (docs/DECISIONS.md R11). A file is never edited once merged.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
