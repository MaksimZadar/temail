package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MaksimZadar/temail/internal/model"
)

func TestCacheStatsCountsPerAccount(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	for _, acct := range []string{"main", "other"} {
		if err := st.UpsertMailboxes(ctx, acct, []model.Mailbox{{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	var hs []model.MessageHeader
	for u := uint32(1); u <= 3; u++ {
		hs = append(hs, model.MessageHeader{Mailbox: "INBOX", UIDValidity: 1, UID: u, Date: time.Unix(int64(u), 0)})
	}
	if err := st.UpsertHeaders(ctx, "main", hs); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertHeaders(ctx, "other", hs[:1]); err != nil {
		t.Fatal(err)
	}
	if err := st.SetBody(ctx, MessageKey{Account: "main", Mailbox: "INBOX", UIDValidity: 1, UID: 2}, Body{Text: "x"}); err != nil {
		t.Fatal(err)
	}

	got, err := st.CacheStats(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if got.Messages != 3 || got.Bodies != 1 {
		t.Fatalf("main: messages=%d bodies=%d, want 3 and 1", got.Messages, got.Bodies)
	}
	other, err := st.CacheStats(ctx, "other")
	if err != nil {
		t.Fatal(err)
	}
	if other.Messages != 1 || other.Bodies != 0 {
		t.Fatalf("other account leaked into main's counts: %+v", other)
	}
	none, err := st.CacheStats(ctx, "nobody")
	if err != nil {
		t.Fatal(err)
	}
	if none.Messages != 0 || none.Bodies != 0 {
		t.Fatalf("unknown account must count 0, got %+v", none)
	}
}

// sizeOf returns the size of name, or 0 when it does not exist.
func sizeOf(t *testing.T, name string) int64 {
	t.Helper()
	fi, err := os.Stat(name)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func TestCacheStatsBytesIsDBPlusWALPlusSHM(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// The WAL and SHM files change size with the writer, so compare against
	// their sizes at the moment of the call (no writes happen in between).
	sum := func() int64 {
		return sizeOf(t, path) + sizeOf(t, path+"-wal") + sizeOf(t, path+"-shm")
	}
	got, err := st.CacheStats(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if got.Bytes != sum() {
		t.Fatalf("bytes = %d, want db+wal+shm = %d", got.Bytes, sum())
	}
}
