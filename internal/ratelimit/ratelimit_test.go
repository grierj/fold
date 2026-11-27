package ratelimit

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fold/internal/clock"
)

func TestHourlyAndDaily(t *testing.T) {
	c := clock.NewFake(time.Unix(0, 0))
	l := New(c, Rule{N: 2, Per: time.Hour}, Rule{N: 3, Per: 24 * time.Hour})
	got := []bool{l.Allow("a"), l.Allow("a"), l.Allow("a")}
	if !got[0] || !got[1] || got[2] {
		t.Fatalf("hourly limit: %v", got)
	}
	if !l.Allow("b") {
		t.Fatal("one key's limit spent another's")
	}
	c.Advance(time.Hour)
	if !l.Allow("a") || l.Allow("a") {
		t.Fatal("the daily limit should stop the fourth")
	}
	c.Advance(24 * time.Hour)
	if !l.Allow("a") {
		t.Fatal("a new day")
	}
}

func TestCheckAndSpendIsAtomic(t *testing.T) {
	l := New(clock.NewFake(time.Unix(0, 0)), Rule{N: 5, Per: time.Hour})
	var ok atomic.Int32
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Allow("k") {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 5 {
		t.Fatalf("%d allowed, want 5", ok.Load())
	}
}
