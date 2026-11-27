package keys

import (
	"bytes"
	"strings"
	"testing"
)

func TestSealOpenAndPurposeSeparation(t *testing.T) {
	k := FromBytes(bytes.Repeat([]byte{7}, 32))
	s := k.Seal(Settings, []byte("smtp password"))
	got, prev, err := k.Open(Settings, s)
	if err != nil || prev || string(got) != "smtp password" {
		t.Fatalf("Open = %q, %v, %v", got, prev, err)
	}
	if _, _, err := k.Open(Jobs, s); err == nil {
		t.Fatal("a value sealed for one purpose opened under another")
	}
	if bytes.Equal(k.MAC(CSRF, []byte("x")), k.MAC(Setup, []byte("x"))) {
		t.Fatal("two purposes share a key")
	}
}

func TestRotation(t *testing.T) {
	oldHex := strings.Repeat("11", 32)
	newHex := strings.Repeat("22", 32)
	old, _ := Parse(oldHex, "")
	sealed := old.Seal(Settings, []byte("token"))
	mac := old.MAC(CSRF, []byte("m"))

	rot, err := Parse(newHex, oldHex)
	if err != nil {
		t.Fatal(err)
	}
	got, usedPrev, err := rot.Open(Settings, sealed)
	if err != nil || !usedPrev || string(got) != "token" {
		t.Fatalf("rotated Open = %q, %v, %v", got, usedPrev, err)
	}
	if !rot.CheckMAC(CSRF, []byte("m"), mac) {
		t.Fatal("a MAC from the previous key is refused while rotating")
	}
	after, _ := Parse(newHex, "")
	if _, _, err := after.Open(Settings, sealed); err == nil {
		t.Fatal("an old value opened after the previous key was removed")
	}
}

func TestParseRefusesShortOrNonHex(t *testing.T) {
	for _, s := range []string{"", "abc", strings.Repeat("ab", 31), strings.Repeat("zz", 32)} {
		if _, err := Parse(s, ""); err == nil {
			t.Errorf("Parse(%q) accepted", s)
		}
	}
}
