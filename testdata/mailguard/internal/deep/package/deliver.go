package example

import (
	"context"

	"fold/internal/mail"
)

func deliver(ctx context.Context, msg mail.Message) error {
	return shared.Send(ctx, msg)
}

func deliverResult(ctx context.Context, msg mail.Message) error {
	_, sender := mailer()
	return sender.Send(ctx, msg)
}
