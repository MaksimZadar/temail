package mail

import (
	"errors"
	"strings"
	"testing"
)

func TestIsLoopbackHost(t *testing.T) {
	for _, h := range []string{"127.0.0.1", "127.0.0.2", "::1", "localhost", "LOCALHOST"} {
		if !isLoopbackHost(h) {
			t.Errorf("%q should be loopback", h)
		}
	}
	for _, h := range []string{"", "10.0.0.1", "192.0.2.1", "example.com", "0.0.0.0", "::", "127.0.0.1.nip.io"} {
		if isLoopbackHost(h) {
			t.Errorf("%q should not be loopback", h)
		}
	}
}

func TestPinVerifier(t *testing.T) {
	der := []byte("not really a certificate")
	fp := fingerprintOf(der)
	if len(fp) != 64 {
		t.Fatalf("fingerprint length %d", len(fp))
	}
	// The verifier parses the DER first, so a fake certificate is rejected
	// before any pin comparison.
	p := &pinCheck{}
	if err := p.verifier("")([][]byte{der}, nil); err == nil {
		t.Error("unparseable certificate accepted")
	}
	if err := p.verifier("")(nil, nil); err == nil {
		t.Error("empty certificate chain accepted")
	}
	if p.resolve(nil) == nil {
		t.Error("resolve should report the recorded verifier error")
	}
	var nilErr error
	fresh := &pinCheck{}
	if fresh.resolve(nilErr) != nil {
		t.Error("resolve with no recorded error must return the passed error (nil)")
	}
}

func TestErrorTypesMatchSentinels(t *testing.T) {
	u := &UntrustedCertError{Fingerprint: strings.Repeat("a", 64), Subject: "CN=x"}
	if !errors.Is(u, ErrUntrustedCert) || errors.Is(u, ErrCertMismatch) {
		t.Error("UntrustedCertError matches the wrong sentinel")
	}
	m := &CertMismatchError{Pinned: "a", Fingerprint: "b", Subject: "CN=x"}
	if !errors.Is(m, ErrCertMismatch) || errors.Is(m, ErrUntrustedCert) {
		t.Error("CertMismatchError matches the wrong sentinel")
	}
	if !strings.Contains(u.Error(), "confirm the fingerprint") {
		t.Errorf("untrusted message: %v", u)
	}
}
