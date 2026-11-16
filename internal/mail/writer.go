package mail

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/MaksimZadar/temail/internal/config"
)

// ErrExpungeUnsupported means the server lacks UIDPLUS and IMAP4rev2, so a UID
// EXPUNGE cannot be issued. A plain EXPUNGE would remove every \Deleted message
// in the mailbox, so the writer refuses instead.
var ErrExpungeUnsupported = errors.New("the server does not support UID EXPUNGE (UIDPLUS)")

// ErrMoveUnsupported means the server lacks MOVE and IMAP4rev2. go-imap's own
// MOVE fallback would COPY, STORE \Deleted and EXPUNGE, which this package must
// not do for an archive or move, so the writer refuses instead.
var ErrMoveUnsupported = errors.New("the server does not support MOVE")

// CopyUID is the UIDPLUS answer to COPY or MOVE. DestUIDs[i] is the UID that
// SourceUIDs[i] got in the target mailbox. UIDValidity is the target's epoch.
// Empty DestUIDs means the server did not report COPYUID.
type CopyUID struct {
	UIDValidity uint32
	SourceUIDs  []uint32
	DestUIDs    []uint32
}

// Writer is a write-capable Bridge connection. It is a separate type from the
// read-only Client on purpose: the Client interface stays free of write
// methods, and this interface is reviewed as the one place where temail changes
// mailbox state (docs/ARCHITECTURE.md §3.3, §7).
//
// Every method selects the mailbox it needs and runs under one operation lock,
// so calls are serialised. Cancelling the context of a call closes the
// connection; call Connect again afterwards.
type Writer interface {
	// Connect dials Bridge with the same pin and login rules as Client.Connect.
	Connect(ctx context.Context, acct config.BridgeAccount, password string) error

	// Capabilities returns the server's capability names, upper-cased and sorted.
	Capabilities() []string

	// StoreFlags adds and removes flags on uids in mailbox. Flags are global per
	// message in Proton, so \Seen and \Flagged may be set from any mailbox the
	// message is in.
	StoreFlags(ctx context.Context, mailbox string, uids []uint32, add, remove []string) error

	// Move runs UID MOVE from mailbox to target and returns the COPYUID data when
	// the server reports it.
	Move(ctx context.Context, mailbox string, uids []uint32, target string) (CopyUID, error)

	// Copy runs UID COPY from mailbox to target and returns the COPYUID data when
	// the server reports it.
	Copy(ctx context.Context, mailbox string, uids []uint32, target string) (CopyUID, error)

	// ExpungeDeleted sets \Deleted on uids in mailbox and then runs UID EXPUNGE
	// for exactly those UIDs. It must only be called for a Labels/<name> mailbox
	// or Trash/Drafts, never to complete a move or archive (internal/actions
	// enforces that, and a static test checks it).
	ExpungeDeleted(ctx context.Context, mailbox string, uids []uint32) error

	// SearchInternalID returns the UIDs in mailbox whose X-Pm-Internal-Id header
	// equals internalID. It is the verification path when COPYUID is missing.
	SearchInternalID(ctx context.Context, mailbox, internalID string) ([]uint32, error)

	// FetchFlags returns the flags of the given UIDs. UIDs that are not in the
	// mailbox are absent from the map. It is used to verify flag writes and to
	// confirm that a MOVE removed the source UIDs.
	FetchFlags(ctx context.Context, mailbox string, uids []uint32) (map[uint32][]string, error)

	// Close logs out and closes the connection. Closing an unconnected Writer is
	// not an error.
	Close() error
}

// imapWriter implements Writer on top of go-imap/v2.
type imapWriter struct {
	opMu sync.Mutex
	mu   sync.Mutex
	cl   *imapclient.Client
}

// NewWriter returns an unconnected write-capable client.
func NewWriter() Writer {
	return &imapWriter{}
}

var _ Writer = (*imapWriter)(nil)

// Connect implements Writer.
func (w *imapWriter) Connect(ctx context.Context, acct config.BridgeAccount, password string) error {
	w.opMu.Lock()
	defer w.opMu.Unlock()
	if err := w.Close(); err != nil {
		return fmt.Errorf("close previous connection: %w", err)
	}
	cl, err := connectBridge(ctx, acct, password, nil)
	if err != nil {
		return err
	}
	w.mu.Lock()
	w.cl = cl
	w.mu.Unlock()
	return nil
}

// Capabilities implements Writer.
func (w *imapWriter) Capabilities() []string {
	w.mu.Lock()
	cl := w.cl
	w.mu.Unlock()
	if cl == nil {
		return nil
	}
	caps := cl.Caps()
	out := make([]string, 0, len(caps))
	for name := range caps {
		out = append(out, string(name))
	}
	slices.Sort(out)
	return out
}

// StoreFlags implements Writer.
func (w *imapWriter) StoreFlags(ctx context.Context, mailbox string, uids []uint32, add, remove []string) error {
	if mailbox == "" || len(uids) == 0 || (len(add) == 0 && len(remove) == 0) {
		return errors.New("store flags: mailbox, UIDs and flags are required")
	}
	cl, end, err := w.begin(ctx)
	if err != nil {
		return err
	}
	defer end()
	if _, err := selectWritable(cl, mailbox); err != nil {
		return opErr(ctx, err)
	}
	set := uidSetOf(uids)
	if len(add) > 0 {
		if err := cl.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: imapFlags(add)}, nil).Close(); err != nil {
			return opErr(ctx, fmt.Errorf("add flags on UIDs %v in %s: %w", uids, mailbox, err))
		}
	}
	if len(remove) > 0 {
		if err := cl.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsDel, Silent: true, Flags: imapFlags(remove)}, nil).Close(); err != nil {
			return opErr(ctx, fmt.Errorf("remove flags on UIDs %v in %s: %w", uids, mailbox, err))
		}
	}
	return nil
}

// Move implements Writer.
func (w *imapWriter) Move(ctx context.Context, mailbox string, uids []uint32, target string) (CopyUID, error) {
	if mailbox == "" || target == "" || len(uids) == 0 {
		return CopyUID{}, errors.New("move: mailbox, target and UIDs are required")
	}
	cl, end, err := w.begin(ctx)
	if err != nil {
		return CopyUID{}, err
	}
	defer end()
	if !cl.Caps().Has(imap.CapMove) {
		// go-imap's own fallback would COPY, STORE \Deleted and EXPUNGE, which
		// this package must never do for a move; actions falls back to an
		// explicit COPY where that is the right recipe.
		return CopyUID{}, ErrMoveUnsupported
	}
	if _, err := selectWritable(cl, mailbox); err != nil {
		return CopyUID{}, opErr(ctx, err)
	}
	data, err := cl.Move(uidSetOf(uids), target).Wait()
	if err != nil {
		return CopyUID{}, opErr(ctx, fmt.Errorf("move UIDs %v from %s to %s: %w", uids, mailbox, target, err))
	}
	return copyUIDFrom(data.UIDValidity, data.SourceUIDs, data.DestUIDs), nil
}

// Copy implements Writer.
func (w *imapWriter) Copy(ctx context.Context, mailbox string, uids []uint32, target string) (CopyUID, error) {
	if mailbox == "" || target == "" || len(uids) == 0 {
		return CopyUID{}, errors.New("copy: mailbox, target and UIDs are required")
	}
	cl, end, err := w.begin(ctx)
	if err != nil {
		return CopyUID{}, err
	}
	defer end()
	if _, err := selectWritable(cl, mailbox); err != nil {
		return CopyUID{}, opErr(ctx, err)
	}
	data, err := cl.Copy(uidSetOf(uids), target).Wait()
	if err != nil {
		return CopyUID{}, opErr(ctx, fmt.Errorf("copy UIDs %v from %s to %s: %w", uids, mailbox, target, err))
	}
	return copyUIDFrom(data.UIDValidity, data.SourceUIDs, data.DestUIDs), nil
}

// ExpungeDeleted implements Writer.
func (w *imapWriter) ExpungeDeleted(ctx context.Context, mailbox string, uids []uint32) error {
	if mailbox == "" || len(uids) == 0 {
		return errors.New("expunge deleted: mailbox and UIDs are required")
	}
	cl, end, err := w.begin(ctx)
	if err != nil {
		return err
	}
	defer end()
	if !cl.Caps().Has(imap.CapUIDPlus) && !cl.Caps().Has(imap.CapIMAP4rev2) {
		return ErrExpungeUnsupported
	}
	if _, err := selectWritable(cl, mailbox); err != nil {
		return opErr(ctx, err)
	}
	set := uidSetOf(uids)
	if err := cl.Store(set, &imap.StoreFlags{
		Op:     imap.StoreFlagsAdd,
		Silent: true,
		Flags:  []imap.Flag{imap.FlagDeleted},
	}, nil).Close(); err != nil {
		return opErr(ctx, fmt.Errorf("mark UIDs %v deleted in %s: %w", uids, mailbox, err))
	}
	if _, err := cl.UIDExpunge(set).Collect(); err != nil {
		return opErr(ctx, fmt.Errorf("expunge UIDs %v in %s: %w", uids, mailbox, err))
	}
	return nil
}

// SearchInternalID implements Writer.
func (w *imapWriter) SearchInternalID(ctx context.Context, mailbox, internalID string) ([]uint32, error) {
	if mailbox == "" || internalID == "" {
		return nil, errors.New("search internal id: mailbox and id are required")
	}
	cl, end, err := w.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer end()
	if _, err := selectReadOnly(cl, mailbox); err != nil {
		return nil, opErr(ctx, err)
	}
	data, err := cl.UIDSearch(&imap.SearchCriteria{
		Header: []imap.SearchCriteriaHeaderField{{Key: "X-Pm-Internal-Id", Value: internalID}},
	}, nil).Wait()
	if err != nil {
		return nil, opErr(ctx, fmt.Errorf("search %s for X-Pm-Internal-Id: %w", mailbox, err))
	}
	out := make([]uint32, 0, len(data.AllUIDs()))
	for _, uid := range data.AllUIDs() {
		out = append(out, uint32(uid))
	}
	return out, nil
}

// FetchFlags implements Writer.
func (w *imapWriter) FetchFlags(ctx context.Context, mailbox string, uids []uint32) (map[uint32][]string, error) {
	out := make(map[uint32][]string, len(uids))
	if len(uids) == 0 {
		return out, nil
	}
	cl, end, err := w.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer end()
	if _, err := selectReadOnly(cl, mailbox); err != nil {
		return nil, opErr(ctx, err)
	}
	bufs, err := cl.Fetch(uidSetOf(uids), flagFetchOptions()).Collect()
	if err != nil {
		return nil, opErr(ctx, fmt.Errorf("fetch flags from %s: %w", mailbox, err))
	}
	for _, buf := range bufs {
		st := uidStateFromBuffer(buf)
		out[st.UID] = st.Flags
	}
	return out, nil
}

// Close implements Writer.
func (w *imapWriter) Close() error {
	w.mu.Lock()
	cl := w.cl
	w.cl = nil
	w.mu.Unlock()
	if cl == nil {
		return nil
	}
	return cl.Close()
}

// begin takes the operation lock and returns the connection. If ctx ends while
// the operation runs, the connection is closed, so the operation fails.
func (w *imapWriter) begin(ctx context.Context) (*imapclient.Client, func(), error) {
	w.opMu.Lock()
	w.mu.Lock()
	cl := w.cl
	w.mu.Unlock()
	if cl == nil {
		w.opMu.Unlock()
		return nil, nil, ErrNotConnected
	}
	if err := ctx.Err(); err != nil {
		w.opMu.Unlock()
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = cl.Close() })
	return cl, func() {
		stop()
		w.opMu.Unlock()
	}, nil
}

// selectWritable opens a mailbox with SELECT, so STORE, COPY, MOVE and EXPUNGE
// are allowed. Every write goes through here; reads use selectReadOnly.
func selectWritable(cl *imapclient.Client, mailbox string) (*imap.SelectData, error) {
	data, err := cl.Select(mailbox, &imap.SelectOptions{ReadOnly: false}).Wait()
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", mailbox, err)
	}
	return data, nil
}

// uidSetOf builds a UID set from ascending or unsorted UIDs.
func uidSetOf(uids []uint32) imap.UIDSet {
	set := make(imap.UIDSet, 0, len(uids))
	for _, uid := range uids {
		if uid != 0 {
			set.AddNum(imap.UID(uid))
		}
	}
	return set
}

// imapFlags converts flag names (for example `\Seen`) to imap.Flag.
func imapFlags(names []string) []imap.Flag {
	out := make([]imap.Flag, 0, len(names))
	for _, name := range names {
		out = append(out, imap.Flag(name))
	}
	return out
}

// copyUIDFrom converts a go-imap COPYUID answer. A missing set yields an empty
// DestUIDs, which the caller treats as "no COPYUID".
func copyUIDFrom(uidValidity uint32, src, dst imap.NumSet) CopyUID {
	out := CopyUID{UIDValidity: uidValidity}
	if set, ok := src.(imap.UIDSet); ok {
		nums, _ := set.Nums()
		for _, uid := range nums {
			out.SourceUIDs = append(out.SourceUIDs, uint32(uid))
		}
	}
	if set, ok := dst.(imap.UIDSet); ok {
		nums, _ := set.Nums()
		for _, uid := range nums {
			out.DestUIDs = append(out.DestUIDs, uint32(uid))
		}
	}
	return out
}
