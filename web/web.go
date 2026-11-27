// Package web holds the templates and static files, embedded in the binary.
package web

import "embed"

//go:embed templates
var Templates embed.FS

//go:embed static
var Static embed.FS
