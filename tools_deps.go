//go:build tools

// Placeholder: these blank imports keep the dependencies that no package uses
// yet (go-sasl, go-smtp, openai-go, sqlite) in go.mod across `go mod tidy`.
// internal/mail now imports go-imap, go-message, enmime and bluemonday, so they
// are no longer listed here. The `tools` build tag keeps this file out of
// normal builds. Delete it once real code imports each remaining package.
package main

import (
	_ "github.com/emersion/go-sasl"
	_ "github.com/emersion/go-smtp"
	_ "github.com/openai/openai-go/v3"
	_ "modernc.org/sqlite"
)
