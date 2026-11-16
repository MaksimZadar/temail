package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/mail"
)

// CredsKey identifies the credentials for the auth latch: a SHA-256 over the
// connection settings and the password. The password is never stored or logged.
// Every login path of an account uses this key, so a refusal on one connection
// latches the others.
func CredsKey(acct config.BridgeAccount, pw string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%d\x00%s\x00%s\x00%s", acct.Host, acct.IMAPPort, acct.Username, acct.CertFingerprint, pw)
	return hex.EncodeToString(h.Sum(nil))
}

// AuthLatch is one strike for every login path. Bridge counts failed logins
// across all clients and locks out after a few, so once a login is refused,
// nothing may try again with the same credentials: not the sync, not the poll
// connection, and not the IDLE connection.
//
// The latch holds the refused error and a key that identifies the credentials
// (a hash the caller computes; the password itself is never kept). Check
// returns the error while the presented key matches. A different key, meaning
// the user changed the account or the password, clears the latch. An app
// restart creates a new latch.
//
// A nil *AuthLatch is valid and never latches, so tests and CLIs can omit it.
type AuthLatch struct {
	mu  sync.Mutex
	key string
	err error
}

// NewAuthLatch returns an empty latch.
func NewAuthLatch() *AuthLatch { return &AuthLatch{} }

// Trip records err if it is an authentication failure, for the credentials key.
// Other errors are not latched: they are retried as before.
func (l *AuthLatch) Trip(key string, err error) {
	if l == nil || !errors.Is(err, mail.ErrAuthFailed) {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.key = key
	l.err = err
}

// Check returns the latched error when the latch was tripped with key. It
// returns nil when the latch is empty. When the latch was tripped with another
// key, it is cleared and Check returns nil.
func (l *AuthLatch) Check(key string) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err == nil {
		return nil
	}
	if l.key != key {
		l.key, l.err = "", nil
		return nil
	}
	return l.err
}

// Tripped reports whether the latch holds an error. It does not change it.
func (l *AuthLatch) Tripped() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err != nil
}
