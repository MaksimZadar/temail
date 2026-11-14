package mail

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net"
	"strings"
)

// Certificate pinning for Proton Bridge.
//
// Bridge serves a self-signed leaf certificate, so the normal chain check
// cannot succeed. The trust decision is instead the SHA-256 fingerprint of the
// leaf certificate, stored per account in config.json.
//
// The TLS config sets InsecureSkipVerify, which only turns off the standard
// chain check. It is allowed only for loopback hosts (see isLoopbackHost), and
// VerifyPeerCertificate then enforces the pin on every handshake. Connect
// refuses any other host before dialing, so the skip cannot apply to a remote
// server.

// fingerprintOf returns the lowercase hex SHA-256 of the DER certificate.
func fingerprintOf(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// isLoopbackHost reports whether host names the local machine: a loopback IP
// or "localhost".
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// pinCheck records the outcome of the certificate check during a handshake so
// Connect can return the typed error instead of a wrapped TLS alert.
type pinCheck struct {
	err error
}

// verifier returns a VerifyPeerCertificate callback that enforces pinned, which
// must be a normalised (lowercase) fingerprint or empty.
func (p *pinCheck) verifier(pinned string) func([][]byte, [][]*x509.Certificate) error {
	return func(raw [][]byte, _ [][]*x509.Certificate) error {
		if len(raw) == 0 {
			p.err = errors.New("bridge sent no TLS certificate")
			return p.err
		}
		leaf, err := x509.ParseCertificate(raw[0])
		if err != nil {
			p.err = err
			return err
		}
		fp := fingerprintOf(raw[0])
		subject := leaf.Subject.String()
		switch {
		case pinned == "":
			p.err = &UntrustedCertError{Fingerprint: fp, Subject: subject}
		case fp != pinned:
			p.err = &CertMismatchError{Pinned: pinned, Fingerprint: fp, Subject: subject}
		}
		return p.err
	}
}

// resolve returns the pin error if the handshake failed because of it, and
// otherwise err.
func (p *pinCheck) resolve(err error) error {
	if p.err != nil {
		return p.err
	}
	return err
}

// tlsConfigFor builds the client TLS config for a loopback Bridge with the
// given pinned fingerprint (empty when not pinned).
func tlsConfigFor(pinned string, p *pinCheck) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Loopback-only skip of the chain check; the pin is enforced by
		// VerifyPeerCertificate below. See the package comment above.
		InsecureSkipVerify:    true,
		VerifyPeerCertificate: p.verifier(pinned),
	}
}
