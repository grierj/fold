package accounts

import (
	"errors"
	"testing"
	"time"
)

func date(s string) *time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return &t
}

func TestAgeOnBirthdays(t *testing.T) {
	birth := time.Date(2013, 6, 15, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		on   string
		want int
	}{
		{"2026-06-14", 12},
		{"2026-06-15", 13},
		{"2031-06-14", 17},
		{"2031-06-15", 18},
	}
	for _, c := range cases {
		on, _ := time.Parse("2006-01-02", c.on)
		if got := AgeOn(birth, on); got != c.want {
			t.Errorf("AgeOn on %s: got %d, want %d", c.on, got, c.want)
		}
	}
}

func TestClassifyAgeGate(t *testing.T) {
	today := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		band string
		born *time.Time
		want string
		err  error
	}{
		{"adult, no date needed", BandAdult, nil, "adult", nil},
		{"adult with an adult date", BandAdult, date("2000-01-01"), "adult", nil},
		{"adult band with a teen date", BandAdult, date("2012-01-01"), "", ErrAgeMismatch},
		{"teen on 13th birthday", BandTeen, date("2013-10-14"), "teen", nil},
		{"teen at 17", BandTeen, date("2009-10-14"), "teen", nil},
		{"teen band at 18 is a mismatch", BandTeen, date("2008-10-14"), "", ErrAgeMismatch},
		{"teen band at 12 is a mismatch", BandTeen, date("2014-10-14"), "", ErrAgeMismatch},
		{"teen needs a birth date", BandTeen, nil, "", ErrAgeMismatch},
		{"under 13 stores nothing", BandUnder13, date("2020-01-01"), "", ErrUnder13},
		{"under 13 without a date", BandUnder13, nil, "", ErrUnder13},
		{"unknown band", "child", nil, "", ErrAgeMismatch},
	}
	for _, c := range cases {
		got, err := Classify(c.band, c.born, today)
		if !errors.Is(err, c.err) || got != c.want {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", c.name, got, err, c.want, c.err)
		}
	}
}

func TestParseBirthDate(t *testing.T) {
	if d, err := ParseBirthDate(""); d != nil || err != nil {
		t.Errorf("empty: got (%v, %v)", d, err)
	}
	if d, err := ParseBirthDate("2013-02-29"); err == nil || d != nil {
		t.Errorf("not a real date: got (%v, %v)", d, err)
	}
	if d, err := ParseBirthDate("2013-06-15"); err != nil || d == nil || d.Format("2006-01-02") != "2013-06-15" {
		t.Errorf("valid: got (%v, %v)", d, err)
	}
}
