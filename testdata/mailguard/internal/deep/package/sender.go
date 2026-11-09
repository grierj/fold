package example

import "fold/internal/mail"

type App struct{ Mailer mail.Sender }

// Inferred package variables must remain visible to the other file's scan.
var shared = (&App{}).Mailer

// The sender is the second result, declared in a different source file.
func mailer() (error, mail.Sender) { return nil, nil }
