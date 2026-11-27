package accounts

import (
	"context"
	"log/slog"

	"fold/internal/write"
)

// SignInQueue hands sign-in requests from the form to one worker. The form
// answers as soon as the address is queued, so neither its answer nor its
// timing says whether the address has an account, and a stranger's request
// never waits on the writer. A full queue drops the request; the person sees
// the usual answer and can ask again. Requests in memory are lost on restart,
// which is the same.
type SignInQueue struct {
	exec *write.Executor
	log  *slog.Logger
	ch   chan string
}

func NewSignInQueue(e *write.Executor, log *slog.Logger) *SignInQueue {
	return &SignInQueue{exec: e, log: log, ch: make(chan string, 256)}
}

// Add queues a normalized, validated address. It never blocks.
func (q *SignInQueue) Add(email string) {
	select {
	case q.ch <- email:
	default:
		q.log.Warn("signin: queue full, request dropped")
	}
}

// Run works the queue until ctx ends.
func (q *SignInQueue) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case email := <-q.ch:
			q.handle(ctx, email)
		}
	}
}

func (q *SignInQueue) handle(ctx context.Context, email string) {
	if err := q.exec.Run(ctx, write.System(), RequestSignIn{Email: email}); err != nil {
		q.log.Error("signin: request", "err", err)
	}
}

// Drain handles everything queued, then returns. Tests use it in place of
// Run, so nothing happens behind their back.
func (q *SignInQueue) Drain(ctx context.Context) {
	for {
		select {
		case email := <-q.ch:
			q.handle(ctx, email)
		default:
			return
		}
	}
}
