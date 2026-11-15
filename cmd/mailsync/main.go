// Command mailsync is a manual tool that runs a full sync from Proton Bridge
// into the local SQLite cache. It is not run in CI and is not used by the app.
//
// It loads config.json through config.DefaultManager, reads the Bridge password
// from the keyring (or TEMAIL_BRIDGE_PASSWORD), and runs internal/sync. It is
// read-only against the mailbox: it only lists, reads headers and reads bodies.
// It writes only the cache (store.DefaultPath unless -cache is set). Ctrl-C is
// safe, and running it again resumes where it stopped. The password is never
// printed.
//
// If the Bridge certificate is not pinned yet, mailsync prints its SHA-256 and
// subject. With -trust it pins that fingerprint in config.json, as mailprobe does.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/secrets"
	"github.com/MaksimZadar/temail/internal/store"
	mailsync "github.com/MaksimZadar/temail/internal/sync"
)

func main() {
	accountID := flag.String("account", "", "account ID from config.json (default: the first account)")
	trust := flag.Bool("trust", false, "pin the presented certificate fingerprint into config.json if it is not pinned yet")
	cachePath := flag.String("cache", "", "cache database path (default: the user data directory, see store.DefaultPath)")
	bodyDelay := flag.Duration("body-delay", mailsync.DefaultBodyDelay, "pause between body fetches")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, *accountID, *trust, *cachePath, *bodyDelay); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "mailsync: interrupted; the cache keeps what was committed. Run again to resume.")
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "mailsync:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, accountID string, trust bool, cachePath string, bodyDelay time.Duration) error {
	mgr, err := config.DefaultManager()
	if err != nil {
		return err
	}
	cfg, err := mgr.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	idx, err := pickAccount(cfg, accountID)
	if err != nil {
		return err
	}

	password, err := secrets.NewKeyringStore().GetBridgePassword(cfg.Bridge[idx].ID)
	if err != nil {
		if errors.Is(err, secrets.ErrNotFound) {
			return fmt.Errorf("no Bridge password for account %q: store it in the keyring or set %s",
				cfg.Bridge[idx].ID, secrets.EnvBridgePassword)
		}
		return fmt.Errorf("read Bridge password: %w", err)
	}

	if err := ensurePinned(ctx, mgr, &cfg, idx, password, trust); err != nil {
		return err
	}
	acct := cfg.Bridge[idx]

	if cachePath == "" {
		if cachePath, err = store.DefaultPath(); err != nil {
			return err
		}
	}
	st, err := store.Open(cachePath)
	if err != nil {
		return fmt.Errorf("open cache: %w", err)
	}
	defer st.Close()
	fmt.Printf("Cache: %s\n", cachePath)

	client := mail.NewClient()
	defer client.Close()
	syn, err := mailsync.New(st, client, func(ctx context.Context) error {
		return client.Connect(ctx, acct, password)
	}, mailsync.Options{
		Account:    acct.ID,
		BodyDelay:  bodyDelay,
		OnProgress: printProgress(),
	})
	if err != nil {
		return err
	}

	res, runErr := syn.Run(ctx)
	fmt.Printf("\nDone: %d mailboxes, %d headers fetched, %d deleted on server, %d bodies fetched, %d body failures.\n",
		res.Mailboxes, res.HeadersFetched, res.Deleted, res.BodiesFetched, res.BodiesFailed)
	for _, me := range res.MailboxErrors {
		fmt.Fprintf(os.Stderr, "mailsync: mailbox %s failed: %v\n", me.Mailbox, me.Err)
	}
	if runErr != nil && !errors.Is(runErr, mailsync.ErrPartial) {
		return fmt.Errorf("sync: %w", runErr)
	}
	if err := printCounts(ctx, st, acct.ID); err != nil {
		return err
	}
	if runErr != nil {
		return fmt.Errorf("%w; the rest of the cache was updated, fix the mailboxes above and run again", runErr)
	}
	return nil
}

// ensurePinned makes one connection attempt to find out whether the Bridge
// certificate is pinned. If it is not, it pins the presented fingerprint when
// trust is set, and saves config.json. Otherwise it returns an error.
func ensurePinned(ctx context.Context, mgr *config.Manager, cfg *config.Config, idx int, password string, trust bool) error {
	c := mail.NewClient()
	err := c.Connect(ctx, cfg.Bridge[idx], password)
	_ = c.Close()
	if err == nil {
		return nil
	}
	var untrusted *mail.UntrustedCertError
	if !errors.As(err, &untrusted) {
		return err
	}
	fmt.Printf("Bridge certificate is not pinned yet.\n  SHA-256: %s\n  Subject: %s\n",
		untrusted.Fingerprint, untrusted.Subject)
	fmt.Println("Compare the SHA-256 with the value shown in Proton Bridge before trusting it.")
	if !trust {
		return errors.New("certificate not trusted; re-run with -trust after checking the fingerprint")
	}
	cfg.Bridge[idx].CertFingerprint = untrusted.Fingerprint
	if err := mgr.Save(*cfg); err != nil {
		return fmt.Errorf("save pinned fingerprint: %w", err)
	}
	fmt.Printf("Pinned in %s.\n", mgr.Path())
	return nil
}

// pickAccount returns the index of the account with the given ID, or of the
// first account when id is empty.
func pickAccount(cfg config.Config, id string) (int, error) {
	if len(cfg.Bridge) == 0 {
		return 0, errors.New("no Bridge account in config.json; add one first")
	}
	if id == "" {
		return 0, nil
	}
	for i, a := range cfg.Bridge {
		if a.ID == id {
			return i, nil
		}
	}
	return 0, fmt.Errorf("no account %q in config.json", id)
}

// printProgress prints account-level states, header windows, and every 50th
// body (and the last one of each mailbox), so a large mailbox does not flood
// the terminal.
func printProgress() func(mailsync.Progress) {
	return func(p mailsync.Progress) {
		switch {
		case p.State == mailsync.StateHeaders && p.Mailbox != "":
			fmt.Printf("  %-30s headers %d/%d\n", p.Mailbox, p.HeadersDone, p.HeadersTotal)
		case p.State == mailsync.StateBodies && p.Mailbox != "":
			if p.BodiesDone%50 == 0 || p.BodiesDone == p.BodiesTotal {
				fmt.Printf("  %-30s bodies  %d/%d\n", p.Mailbox, p.BodiesDone, p.BodiesTotal)
			}
		case p.Mailbox != "":
			fmt.Printf("%s %s\n", p.State, p.Mailbox)
		default:
			fmt.Printf("%s\n", p.State)
		}
	}
}

// printCounts prints the cached message and body count per mailbox.
func printCounts(ctx context.Context, st store.Store, account string) error {
	counts, err := st.Counts(ctx, account)
	if err != nil {
		return fmt.Errorf("read cache counts: %w", err)
	}
	fmt.Printf("%-36s %9s %9s\n", "MAILBOX", "MESSAGES", "BODIES")
	for _, c := range counts {
		fmt.Printf("%-36s %9d %9d\n", c.Mailbox, c.Messages, c.Bodies)
	}
	return nil
}
