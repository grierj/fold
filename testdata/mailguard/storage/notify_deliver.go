package storage

import (
	"context"

	delivery "fold/internal/mail"
)

type App struct{ Mailer delivery.Sender }
type storedSender struct{ sender delivery.Sender }

// Sharing a basename with an allowed root file grants no permission here.
func deliver(app *App, ctx context.Context, msg delivery.Message) error {
	s := storedSender{sender: app.Mailer}
	return s.sender.Send(ctx, msg)
}
