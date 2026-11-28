package events_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"fold/internal/events"
)

var update = flag.Bool("update", false, "rewrite the .ics fixtures in testdata")

// The calendar file against fixtures: a date in its own zone, text that
// must be escaped and folded, a date across midnight and a daylight-saving
// change, and a zone that cannot be loaded, written in UTC.
func TestICS(t *testing.T) {
	boise, _ := time.LoadLocation("America/Boise")
	stamp := time.Date(2026, 11, 25, 17, 0, 0, 0, time.UTC)
	cases := map[string][]events.ICSEvent{
		"one-date": {{UID: "d1@church.example.org", Stamp: stamp, Title: "Tuesday study", Description: "We read a chapter a week.\nCoffee at 6:45.",
			Link: "https://church.example.org/dates/d1", Video: "https://video.example.org/room", Location: "Room 4",
			Zone: "America/Boise", Start: time.Date(2026, 12, 1, 19, 0, 0, 0, boise), End: time.Date(2026, 12, 1, 20, 30, 0, 0, boise), Status: "CONFIRMED", Sequence: 3, Modified: time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC)}},
		"escaping": {{UID: "d2@church.example.org", Stamp: stamp, Title: "Dinner, study; prayer\\worship\x00\x1b",
			Description: "First line\r\nSecond, line; with \\ backslash\n\nÜber café — naïve résumé, a long line that must fold at seventy-five octets without splitting a character",
			Link:        "https://church.example.org/dates/d2", Location: "Smith home, 12 Oak St; ring twice",
			Zone: "America/Boise", Start: time.Date(2026, 12, 2, 18, 0, 0, 0, boise), End: time.Date(2026, 12, 2, 19, 30, 0, 0, boise), Status: "TENTATIVE"}},
		"overnight": {{UID: "d3@church.example.org", Stamp: stamp, Title: "Lock-in", Link: "https://church.example.org/dates/d3",
			Zone: "America/Boise", Start: time.Date(2027, 3, 13, 22, 0, 0, 0, boise), End: time.Date(2027, 3, 14, 6, 0, 0, 0, boise)}},
		"utc": {{UID: "d4@church.example.org", Stamp: stamp, Title: "Picnic", Link: "https://church.example.org/dates/d4",
			Zone: "Nowhere/Atlantis", Start: time.Date(2026, 12, 5, 19, 0, 0, 0, time.UTC), End: time.Date(2026, 12, 5, 21, 0, 0, 0, time.UTC)}},
	}
	cases["feed"] = append(append([]events.ICSEvent{}, cases["one-date"]...), cases["utc"]...)
	cases["feed"][1].Status = "CANCELLED"
	for name, list := range cases {
		got := events.ICS("", 0, list)
		if name == "feed" {
			got = events.ICS("Cloverdale, a church", time.Hour, list)
		}
		path := filepath.Join("testdata", name+".ics")
		if *update {
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s:\n%s\nwant:\n%s", name, got, want)
		}
		for _, line := range strings.Split(strings.TrimSuffix(string(got), "\r\n"), "\r\n") {
			if len(line) > 75 || !utf8.ValidString(line) || strings.ContainsAny(line, "\n\r\x00\x1b") {
				t.Errorf("%s: a bad line %q", name, line)
			}
		}
	}
}
