// Package ratelimit counts requests in memory, in the one process.
//
// The decision for this project (docs/HAZARDS.md section 9): counters live in
// memory, never in the database, so an anonymous request never queues behind
// the writer. A restart forgets them, which is acceptable at this size: the
// limits exist to stop one person flooding another's inbox and to slow
// guessing, and each limit is far below what a restart loop could add.
// Reaching a limit is logged; the person sees the form's usual answer.
package ratelimit

import (
	"sync"
	"time"

	"fold/internal/clock"
)

// Rule is at most N in each window of Per.
type Rule struct {
	N   int
	Per time.Duration
}

type window struct {
	start time.Time
	count int
}

// Limiter checks and spends in one step, under one lock, so parallel
// requests cannot slip past a check (docs/HAZARDS.md section 4).
type Limiter struct {
	mu    sync.Mutex
	clock clock.Clock
	rules []Rule
	seen  map[string][]window
	// maxKeys bounds memory (about 40 MB at the worst). When it is reached a
	// new key makes room by dropping another, so a stranger with many
	// networks can reset some counters but never shut everyone out.
	maxKeys int
	swept   time.Time
}

func New(c clock.Clock, rules ...Rule) *Limiter {
	return &Limiter{clock: c, rules: rules, seen: map[string][]window{}, maxKeys: 100_000}
}

// Allow spends one unit for key if every rule has room, and reports whether
// it did. A refused request spends nothing.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	l.sweep(now)
	ws, ok := l.seen[key]
	if !ok {
		if len(l.seen) >= l.maxKeys {
			for k := range l.seen { // map order is unpredictable: a random key
				delete(l.seen, k)
				break
			}
		}
		ws = make([]window, len(l.rules))
	}
	for i, r := range l.rules {
		if now.Sub(ws[i].start) >= r.Per {
			ws[i] = window{start: now}
		}
		if ws[i].count >= r.N {
			l.seen[key] = ws
			return false
		}
	}
	for i := range ws {
		ws[i].count++
	}
	l.seen[key] = ws
	return true
}

// sweep drops keys whose every window has passed, at most once a minute.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.swept) < time.Minute {
		return
	}
	l.swept = now
	for k, ws := range l.seen {
		live := false
		for i, r := range l.rules {
			if now.Sub(ws[i].start) < r.Per {
				live = true
			}
		}
		if !live {
			delete(l.seen, k)
		}
	}
}
