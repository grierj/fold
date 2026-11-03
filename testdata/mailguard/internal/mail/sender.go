package mail

import (
	"context"

	"fold/internal/mail"
)

// Actual sender implementations own provider calls.
func deliver(sender mail.Sender, ctx context.Context, msg mail.Message) error {
	return sender.Send(ctx, msg)
}
