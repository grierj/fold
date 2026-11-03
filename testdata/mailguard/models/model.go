package models

import (
	"context"

	"fold/internal/mail"
)

// A sender parameter must not hide an application send in a non-root package.
func deliver(sender mail.Sender, ctx context.Context, msg mail.Message) error {
	return sender.Send(ctx, msg)
}
