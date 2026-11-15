//go:build tools

// Placeholder: these blank imports keep the dependencies that no package uses
// yet (go-sasl, go-smtp, openai-go) in go.mod across `go mod tidy`.
// internal/mail imports go-imap, go-message, enmime and bluemonday, and
// internal/store imports modernc.org/sqlite, so they are not listed here. The
// `tools` build tag keeps this file out of normal builds. Delete it once real
// code imports each remaining package.
package main

import (
	_ "github.com/emersion/go-sasl"
	_ "github.com/emersion/go-smtp"
	_ "github.com/openai/openai-go/v3"
)
