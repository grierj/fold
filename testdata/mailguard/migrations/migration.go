package migrations

import (
	"context"

	"fold/internal/mail"
)

type App struct{ Mailer mail.Sender }

func (a *App) sender() mail.Sender { return a.Mailer }

func deliver(a *App, ctx context.Context, msg mail.Message) error {
	return a.sender().Send(ctx, msg)
}

func runInBackground(f func()) { go f() }
