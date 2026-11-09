package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func setBusyRetry(t *testing.T, window time.Duration, backoff [busyRetryTries - 1]time.Duration) {
	t.Helper()
	oldWindow, oldBackoff := busyRetryWindow, busyRetryBackoff
	busyRetryWindow, busyRetryBackoff = window, backoff
	t.Cleanup(func() { busyRetryWindow, busyRetryBackoff = oldWindow, oldBackoff })
}

func TestBusyRetryPolicy(t *testing.T) {
	t.Run("at most three attempts", func(t *testing.T) {
		setBusyRetry(t, time.Minute, [2]time.Duration{time.Millisecond, time.Millisecond})
		r := newBusyRetry(context.Background())
		attempts := 1
		for {
			again, err := r.again()
			if err != nil {
				t.Fatal(err)
			}
			if !again {
				break
			}
			attempts++
		}
		if attempts != busyRetryTries {
			t.Errorf("%d attempts, want %d", attempts, busyRetryTries)
		}
	})
	t.Run("a spent window starts nothing and waits no backoff", func(t *testing.T) {
		setBusyRetry(t, time.Millisecond, [2]time.Duration{time.Hour, time.Hour})
		r := newBusyRetry(context.Background())
		time.Sleep(5 * time.Millisecond)
		start := time.Now()
		if again, err := r.again(); again || err != nil {
			t.Fatalf("again = %v, %v; want false, nil", again, err)
		}
		if waited := time.Since(start); waited > time.Second {
			t.Errorf("waited %v for a backoff after the window closed", waited)
		}
	})
	t.Run("the window closing during the backoff starts nothing", func(t *testing.T) {
		setBusyRetry(t, 20*time.Millisecond, [2]time.Duration{50 * time.Millisecond, 50 * time.Millisecond})
		r := newBusyRetry(context.Background())
		if again, err := r.again(); again || err != nil {
			t.Fatalf("again = %v, %v; want false, nil", again, err)
		}
	})
	t.Run("a context that ends during the backoff returns its error", func(t *testing.T) {
		setBusyRetry(t, time.Minute, [2]time.Duration{time.Hour, time.Hour})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		again, err := newBusyRetry(ctx).again()
		if again || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("again = %v, %v; want false and the deadline", again, err)
		}
	})
}
