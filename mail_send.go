package main

import (
	"context"

	"fold/internal/mail"
)

// The one place application mail reaches a provider. Notices
// (notify_deliver.go), announcements (messages_send.go), direct account mail
// (mail_jobs.go) and the composer's test send (api_message_preview.go) each
// decide what an outcome means for their own ledger; how a send ended is
// decided here once.

// sendOutcome is how one send ended.
type sendOutcome int

const (
	// sendSent: the provider took it.
	sendSent sendOutcome = iota
	// sendSuppressed: the address is suppressed (DEC-112); nothing was sent.
	sendSuppressed
	// sendNotConfigured: there is no email service. Retrying in this process
	// can't help; a restart with a provider set can.
	sendNotConfigured
	// sendPermanent: the provider (or the message) refused for good.
	sendPermanent
	// sendTransient: it may work later.
	sendTransient
)

// sendResult is a send's outcome, the provider's id for it (when sent) and
// the error behind a send that did not go out.
type sendResult struct {
	outcome    sendOutcome
	providerID string
	err        error
}

// mailSender is the configured sender; with none, the unconfigured sender, so
// every path records the missing service the same way.
func (a *App) mailSender() mail.Sender {
	if a.Mailer == nil {
		return mail.NoSender{}
	}
	return a.Mailer
}

// sendTracked sends msg through sender and classifies the result. reasons are
// the address's open suppression reasons when the caller already read them
// (it then passes the sender with the suppression wrapper removed,
// mail.Unwrap); a caller that sends through the wrapper passes none.
func sendTracked(ctx context.Context, sender mail.Sender, msg mail.Message, reasons []string) sendResult {
	if err := mail.Blocked(reasons, msg); err != nil {
		return sendResult{outcome: sendSuppressed, err: err}
	}
	providerID, err := sender.SendEmail(ctx, msg)
	switch {
	case err == nil:
		return sendResult{outcome: sendSent, providerID: providerID}
	case mail.IsSuppressed(err):
		return sendResult{outcome: sendSuppressed, err: err}
	case mail.NotConfigured(err):
		return sendResult{outcome: sendNotConfigured, err: err}
	case mail.IsPermanent(err):
		return sendResult{outcome: sendPermanent, err: err}
	}
	return sendResult{outcome: sendTransient, err: err}
}
