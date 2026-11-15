package mail

import (
	"context"
	"fmt"
	"sort"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// ListUIDs implements Client. It runs one UID FETCH 1:* (UID FLAGS). The
// mailbox is opened with EXAMINE, so the command is read-only.
func (c *imapClient) ListUIDs(ctx context.Context, mailbox string) (UIDListing, error) {
	cl, end, err := c.begin(ctx)
	if err != nil {
		return UIDListing{}, err
	}
	defer end()

	data, err := selectReadOnly(cl, mailbox)
	if err != nil {
		return UIDListing{}, opErr(ctx, err)
	}
	// One FETCH covers the whole mailbox: "UID 1:*" returns every message,
	// with no SEARCH and no per-batch round trip.
	bufs, err := cl.Fetch(imap.UIDSet{{Start: 1, Stop: 0}}, flagFetchOptions()).Collect()
	if err != nil {
		return UIDListing{}, opErr(ctx, fmt.Errorf("fetch flags from %s: %w", mailbox, err))
	}
	out := UIDListing{UIDValidity: data.UIDValidity, Messages: make([]UIDState, 0, len(bufs))}
	for _, buf := range bufs {
		out.Messages = append(out.Messages, uidStateFromBuffer(buf))
	}
	sort.Slice(out.Messages, func(i, j int) bool { return out.Messages[i].UID < out.Messages[j].UID })
	return out, nil
}

// flagFetchOptions asks for the UID and flags only. It does not use BODY.
func flagFetchOptions() *imap.FetchOptions {
	return &imap.FetchOptions{UID: true, Flags: true}
}

// uidStateFromBuffer converts one FLAGS response to a UIDState.
func uidStateFromBuffer(buf *imapclient.FetchMessageBuffer) UIDState {
	st := UIDState{UID: uint32(buf.UID)}
	for _, f := range buf.Flags {
		st.Flags = append(st.Flags, string(f))
	}
	return st
}
