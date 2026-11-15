//go:build tools

// Placeholder: these blank imports keep the dependencies that no package uses
// yet (go-sasl, go-smtp) in go.mod across `go mod tidy`. internal/mail imports
// go-imap, go-message, enmime and bluemonday, internal/store imports
// modernc.org/sqlite, and internal/llm imports openai-go, so none of those are
// listed here. The `tools` build tag keeps this file out of normal builds.
// Delete it once real code imports each remaining package.
package main

import (
	_ "github.com/emersion/go-sasl"
	_ "github.com/emersion/go-smtp"
)
