package mail

import (
	"regexp"
	"strings"
)

// Proton identity. Bridge gives each message an internal id. The same message
// has a different UID in every mailbox it is in, so the id is what ties the
// copies together. It is read from one of two places, in this order:
//  1. the X-Pm-Internal-Id header;
//  2. the local part of a Message-ID of the form <id@protonmail.internalid>.
//
// If neither is present, the header has no canonical id. The store then keys
// it by a synthetic id that is unique to the placement.

// protonIDRE matches a Message-ID that Bridge builds from the internal id. It
// is applied after the angle brackets are removed, because the envelope gives
// the value without them and the raw header keeps them.
var protonIDRE = regexp.MustCompile(`^([^<>@\s]+)@protonmail\.internalid$`)

// internalIDOf returns the Proton id and whether it is canonical. sec is the raw
// header block, and messageID is the parsed Message-ID from the envelope.
func internalIDOf(sec []byte, messageID string) (string, bool) {
	if id := strings.TrimSpace(headerField(sec, "X-Pm-Internal-Id")); id != "" && !strings.ContainsAny(id, " \t\r\n") {
		return id, true
	}
	mid := strings.TrimSpace(messageID)
	if mid == "" {
		mid = strings.TrimSpace(headerField(sec, "Message-ID"))
	}
	mid = strings.TrimSuffix(strings.TrimPrefix(mid, "<"), ">")
	if m := protonIDRE.FindStringSubmatch(mid); m != nil {
		return m[1], true
	}
	return "", false
}
