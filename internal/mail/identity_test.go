package mail

import "testing"

func TestInternalIDOf(t *testing.T) {
	tests := []struct {
		name      string
		section   string
		messageID string
		wantID    string
		wantCanon bool
	}{
		{"X-Pm-Internal-Id header wins", "X-Pm-Internal-Id: pm-abc\r\nMessage-ID: <rfc@example.org>\r\n\r\n", "rfc@example.org", "pm-abc", true},
		{"Message-ID with protonmail.internalid, envelope form", "\r\n", "pm-123@protonmail.internalid", "pm-123", true},
		{"Message-ID with protonmail.internalid, raw header form", "Message-ID: <pm-456@protonmail.internalid>\r\n\r\n", "", "pm-456", true},
		{"ordinary RFC Message-ID is not canonical", "Message-ID: <rfc@example.org>\r\n\r\n", "rfc@example.org", "", false},
		{"empty header block", "\r\n", "", "", false},
		{"header with whitespace in the id is refused", "X-Pm-Internal-Id: a b\r\n\r\n", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, canon := internalIDOf([]byte(tt.section), tt.messageID)
			if id != tt.wantID || canon != tt.wantCanon {
				t.Fatalf("got (%q, %v), want (%q, %v)", id, canon, tt.wantID, tt.wantCanon)
			}
		})
	}
}
