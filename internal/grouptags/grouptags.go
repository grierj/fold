// Package grouptags is the one rule for comparing group tags. Tags are
// stored comma-joined, so no tag contains a comma. Two tags are the same
// tag when they match ignoring case and outer spaces ("men" never matches
// "women"). The Home tag filter and room suggestions (Rule 3, "shares a
// tag") both compare this way, in Go and in SQL.
package grouptags

import "strings"

// Parse splits comma-joined tags (a stored value, or repeated query values
// joined with commas) into normalized tags: lowercased, trimmed, blanks
// dropped. It returns a non-nil slice.
func Parse(joined string) []string {
	return Normalize(strings.Split(joined, ","))
}

func Normalize(tags []string) []string {
	out := []string{}
	for _, t := range tags {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// MatchAnySQL is the SQL condition "the groups row's stored tags share a
// tag with the ? array". It reads the unqualified tags column, so the query
// selects from groups (or a subquery named so), and takes one argument, an
// Array of normalized tags. It is Parse applied to the column, so it agrees
// with Go for tags as the app stores them: comma-joined, each already trimmed.
const MatchAnySQL = `regexp_split_to_array(lower(tags), '\s*,\s*') && ?::text[]`

// Array writes normalized tags as a Postgres text[] literal, the argument
// MatchAnySQL takes. No tags is the empty array, which matches nothing.
func Array(tags []string) string {
	quoted := make([]string, len(tags))
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	for i, t := range tags {
		quoted[i] = `"` + r.Replace(t) + `"`
	}
	return "{" + strings.Join(quoted, ",") + "}"
}
