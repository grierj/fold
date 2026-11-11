package images

import (
	"errors"
	"testing"
)

func fillSlots(t *testing.T) []func() {
	t.Helper()
	var releases []func()
	t.Cleanup(func() {
		for _, r := range releases {
			r()
		}
	})
	for i := 0; i < MaxConcurrentProcessing; i++ {
		release, err := Admit()
		if err != nil {
			t.Fatalf("slot %d: %v", i, err)
		}
		releases = append(releases, release)
	}
	return releases
}

func TestAdmitRefusesAtOnceWhenFull(t *testing.T) {
	releases := fillSlots(t)
	if _, err := Admit(); !errors.Is(err, ErrBusy) {
		t.Fatalf("full: %v, want ErrBusy", err)
	}
	releases[0]()
	release, err := Admit()
	if err != nil {
		t.Fatalf("after a release: %v", err)
	}
	release()
}

// Releasing twice frees one slot, not two, so a deferred release after an
// explicit one can never let more than the limit run at once.
func TestAdmitReleaseIsOnce(t *testing.T) {
	releases := fillSlots(t)
	releases[0]()
	releases[0]()
	first, err := Admit()
	if err != nil {
		t.Fatalf("one slot was freed: %v", err)
	}
	defer first()
	if _, err := Admit(); !errors.Is(err, ErrBusy) {
		t.Fatalf("a second release freed another holder's slot: %v", err)
	}
}
