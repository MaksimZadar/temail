package mail

import (
	"errors"
	"fmt"
)

// ErrUntrustedCert means the Bridge certificate is not pinned yet. Connect
// returns an *UntrustedCertError that wraps it; use errors.As to read the
// fingerprint and subject so the UI can ask the user to confirm them.
var ErrUntrustedCert = errors.New("bridge certificate is not trusted yet")

// ErrCertMismatch means the Bridge certificate differs from the pinned one. Use
// errors.As to read *CertMismatchError.
var ErrCertMismatch = errors.New("bridge certificate does not match the pinned certificate")

// ErrNotLoopback means the configured host is not a loopback address. temail
// only talks to Bridge on the local machine.
var ErrNotLoopback = errors.New("bridge host must be a loopback address (127.0.0.1, ::1 or localhost)")

// ErrNotConnected is returned by operations before Connect succeeds, or after
// Close.
var ErrNotConnected = errors.New("not connected to Bridge")

// UntrustedCertError reports a Bridge certificate that has not been pinned.
// Fingerprint is the lowercase hex SHA-256 of the DER leaf certificate.
type UntrustedCertError struct {
	Fingerprint string
	Subject     string
}

func (e *UntrustedCertError) Error() string {
	return fmt.Sprintf("bridge presented an unpinned certificate (SHA-256 %s, subject %q); "+
		"confirm the fingerprint in Proton Bridge before trusting it", e.Fingerprint, e.Subject)
}

// Is makes errors.Is(err, ErrUntrustedCert) true.
func (e *UntrustedCertError) Is(target error) bool { return target == ErrUntrustedCert }

// CertMismatchError reports a Bridge certificate that differs from the pinned
// fingerprint. Pinned is the stored value and Fingerprint the one presented.
type CertMismatchError struct {
	Pinned      string
	Fingerprint string
	Subject     string
}

func (e *CertMismatchError) Error() string {
	return fmt.Sprintf("bridge certificate changed: pinned SHA-256 %s, presented SHA-256 %s (subject %q). "+
		"If you reinstalled or reset Proton Bridge, check the new fingerprint in Bridge and re-pin the account. "+
		"Otherwise do not trust this server and do not log in", e.Pinned, e.Fingerprint, e.Subject)
}

// Is makes errors.Is(err, ErrCertMismatch) true.
func (e *CertMismatchError) Is(target error) bool { return target == ErrCertMismatch }
