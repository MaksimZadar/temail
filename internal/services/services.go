// Package services is the thin Wails layer between the webview and the Go
// packages. Every exported method of the three services is bound to the
// frontend and is callable by any JavaScript in the window. Business logic
// lives in internal/bridge, internal/mail, internal/store, internal/sync and
// internal/config; this package only validates arguments, shapes results for
// JSON and emits events. See README.md and docs/ARCHITECTURE.md §3.3.
//
// Rules that every bound method follows:
//   - Read-only against the mailbox. No SMTP, no LLM, no flag or move writes.
//   - Never returns a password, API key or any other secret. The Bridge
//     password flows from the frontend to Go only in SetupService.SaveAccount.
//   - Never logs passwords, and never puts one in an error or an event.
//   - Accepts only validated arguments: IDs, part numbers and mailbox names are
//     checked here, and no filesystem path comes from the frontend.
package services

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/secrets"
)

// ConfigStore is the non-secret configuration store. *config.Manager satisfies it.
type ConfigStore interface {
	Load() (config.Config, error)
	Save(config.Config) error
}

// SecretStore is the keyring. *secrets.KeyringStore satisfies it.
type SecretStore interface {
	secrets.Store
	Probe() error
}

// ClientFactory makes a fresh, unconnected Bridge client. Services call it so
// that tests can supply fakes.
type ClientFactory func() mail.Client

// Emitter sends events to the frontend. The Wails event manager (app.Event)
// satisfies it.
type Emitter interface {
	Emit(name string, data ...any) bool
}

// Event names emitted by SyncService.
const (
	EventSyncProgress = "sync:progress"
	EventSyncDone     = "sync:done"
	EventSyncError    = "sync:error"
)

// Connection test categories returned by SetupService.TestConnection.
const (
	CategoryOK            = "ok"
	CategoryNotConfigured = "not_configured"
	CategoryNoPassword    = "no_password"
	CategoryKeyringDown   = "keyring_unavailable"
	CategoryBridgeUnreach = "bridge_unreachable"
	CategoryUntrustedCert = "untrusted_cert"
	CategoryCertMismatch  = "cert_mismatch"
	CategoryAuthFailed    = "auth_failed"
	CategoryInvalidConfig = "invalid_config"
	CategoryOther         = "other"
)

// Input limits applied at the trust boundary.
const (
	maxMailboxName   = 1024
	maxPartIDLen     = 64
	maxUsernameLen   = 320
	maxPasswordLen   = 1024
	maxHostLen       = 253
	DefaultPageSize  = 50
	MaxAttachmentMiB = 25
	// MaxAttachmentBytes caps the attachment bytes returned to the frontend.
	MaxAttachmentBytes = MaxAttachmentMiB << 20
)

// Timeouts for bound calls that talk to Bridge.
const (
	reachTimeout = time.Second
	probeTimeout = 15 * time.Second
	testTimeout  = 30 * time.Second
	syncStopWait = 15 * time.Second
)

// Errors returned to the frontend. Their messages are safe to show.
var (
	ErrInvalidInput       = errors.New("invalid input")
	ErrInvalidFingerprint = errors.New("fingerprint must be 64 hex characters (SHA-256)")
	ErrFingerprintChanged = errors.New("the Bridge certificate changed since it was probed; probe again and compare the new fingerprint")
	ErrNotCached          = errors.New("message is not in the local cache")
	ErrStaleEpoch         = errors.New("the mailbox was reset on the server; sync again before reading it")
	ErrNoSuchAttachment   = errors.New("no attachment with that part ID")
	ErrAttachmentTooLarge = errors.New("attachment is larger than the display limit")
	ErrSyncStopTimeout    = errors.New("sync did not stop in time")
)

// Dialer opens a TCP connection. net.Dialer satisfies it.
type Dialer func(ctx context.Context, network, address string) (net.Conn, error)

var defaultDialer Dialer = (&net.Dialer{}).DialContext
