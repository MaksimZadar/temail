//go:build tools

// Phase-1 placeholder: these blank imports keep the Phase-2+ dependencies in
// go.mod across `go mod tidy`. The `tools` build tag keeps them out of normal
// builds. Delete this file once real code imports each package.
package main

import (
	_ "github.com/emersion/go-imap/v2"
	_ "github.com/emersion/go-message"
	_ "github.com/emersion/go-sasl"
	_ "github.com/emersion/go-smtp"
	_ "github.com/jhillyerd/enmime"
	_ "github.com/microcosm-cc/bluemonday"
	_ "github.com/openai/openai-go/v3"
	_ "github.com/zalando/go-keyring"
	_ "modernc.org/sqlite"
)
