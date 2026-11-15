package mail

import (
	"context"
	"fmt"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// ListUIDs implements Client. It runs UID SEARCH ALL, then UID FETCH FLAGS in
// batches of BatchSize. Both are read-only; the mailbox is opened with EXAMINE.
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
	uids, err := searchUIDs(cl, AllUIDs())
	if err != nil {
		return UIDListing{}, opErr(ctx, fmt.Errorf("search UIDs in %s: %w", mailbox, err))
	}
	out := UIDListing{UIDValidity: data.UIDValidity, Messages: make([]UIDState, 0, len(uids))}
	for start := 0; start < len(uids); start += BatchSize {
		if err := ctx.Err(); err != nil {
			return UIDListing{}, err
		}
		end := min(start+BatchSize, len(uids))
		bufs, err := cl.Fetch(imap.UIDSetNum(uids[start:end]...), flagFetchOptions()).Collect()
		if err != nil {
			return UIDListing{}, opErr(ctx, fmt.Errorf("fetch flags from %s: %w", mailbox, err))
		}
		for _, buf := range bufs {
			out.Messages = append(out.Messages, uidStateFromBuffer(buf))
		}
	}
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
