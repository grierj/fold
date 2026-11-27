package accounts

import (
	"context"
	"log/slog"

	"fold/internal/write"
)

// Queue hands requests from the public forms (a sign-in link, an account) to
// one worker. A form answers as soon as its request is queued, so neither
// the answer nor its timing says whether the address has an account, and a
// stranger's request never waits on the writer. A full queue drops the
// request; the person sees the usual answer and can ask again. Requests in
// memory are lost on restart, which is the same: each one leads to an email,
// so a person who gets none asks again.
type Queue struct {
	exec *write.Executor
	log  *slog.Logger
	ch   chan write.Command
}

func NewQueue(e *write.Executor, log *slog.Logger) *Queue {
	return &Queue{exec: e, log: log, ch: make(chan write.Command, 256)}
}

// Add queues a command to run as the system. It never blocks.
func (q *Queue) Add(c write.Command) {
	select {
	case q.ch <- c:
	default:
		q.log.Warn("queue full, request dropped", "action", c.Action())
	}
}

// Run works the queue until ctx ends.
func (q *Queue) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case c := <-q.ch:
			q.handle(ctx, c)
		}
	}
}

func (q *Queue) handle(ctx context.Context, c write.Command) {
	if err := q.exec.Run(ctx, write.System(), c); err != nil {
		q.log.Error("queued request", "action", c.Action(), "err", err)
	}
}

// Drain handles everything queued, then returns. Tests use it in place of
// Run, so nothing happens behind their back.
func (q *Queue) Drain(ctx context.Context) {
	for {
		select {
		case c := <-q.ch:
			q.handle(ctx, c)
		default:
			return
		}
	}
}
