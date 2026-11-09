package grouptags

import (
	"os"
	"reflect"
	"testing"

	"fold/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

func TestParse(t *testing.T) {
	for in, want := range map[string][]string{
		"":                         {},
		" , ,":                     {},
		"Men, Young Adults ,BIBLE": {"men", "young adults", "bible"},
		"men":                      {"men"},
	} {
		if got := Parse(in); !reflect.DeepEqual(got, want) {
			t.Errorf("Parse(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSQLAgreesWithGo: the SQL condition and Parse give the same answer for
// stored values as the app writes them (comma-joined, each tag trimmed).
func TestSQLAgreesWithGo(t *testing.T) {
	db := testdb.Shared(t)
	stored := []string{"", "Men", "men,Bible", "Young Adults,Bible Study", "Women", "a b,C", "Men , Women"}
	queries := [][]string{{}, {"men"}, {"MEN"}, {"women"}, {"bible"}, {"young adults"}, {"c"}, {"men", "c"}, {"x"}, {" Bible "}, {`q"t`}}
	for _, s := range stored {
		for _, q := range queries {
			want := false
			have := map[string]bool{}
			for _, tag := range Parse(s) {
				have[tag] = true
			}
			for _, tag := range Normalize(q) {
				want = want || have[tag]
			}
			var got bool
			if err := db.Raw(`SELECT EXISTS (SELECT 1 FROM (SELECT ?::text AS tags) g WHERE `+MatchAnySQL+`)`, s, Array(Normalize(q))).Scan(&got).Error; err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("stored %q, query %q: SQL %v, Go %v", s, q, got, want)
			}
		}
	}
}
