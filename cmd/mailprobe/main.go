// Command mailprobe is a manual smoke test for the read-only Bridge client. It
// is not run in CI.
//
// It loads config.json through config.DefaultManager, reads the Bridge password
// from the keyring (or TEMAIL_BRIDGE_PASSWORD), connects, and prints the
// mailbox list and the newest INBOX subjects. It never writes to the mailbox
// and never prints the password.
//
// If the Bridge certificate is not pinned yet, mailprobe prints the SHA-256 and
// subject. With -trust it pins that fingerprint in config.json. Compare the
// fingerprint with the one Proton Bridge shows before using -trust.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/MaksimZadar/temail/internal/bridge"
	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/secrets"
)

func main() {
	accountID := flag.String("account", "", "account ID from config.json (default: the first account)")
	trust := flag.Bool("trust", false, "pin the presented certificate fingerprint into config.json if it is not pinned yet")
	timeout := flag.Duration("timeout", 60*time.Second, "overall timeout for the probe")
	flag.Parse()

	if err := run(*accountID, *trust, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, "mailprobe:", err)
		os.Exit(1)
	}
}

func run(accountID string, trust bool, timeout time.Duration) error {
	mgr, err := config.DefaultManager()
	if err != nil {
		return err
	}
	cfg, err := mgr.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	idx, err := bridge.PickAccount(cfg, accountID)
	if err != nil {
		return err
	}
	acct := cfg.Bridge[idx]

	password, err := bridge.Password(secrets.NewKeyringStore(), acct.ID)
	if err != nil {
		return fmt.Errorf("read Bridge password: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	client, err := connectWithPin(ctx, mgr, cfg, idx, password, trust)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := printMailboxes(ctx, client); err != nil {
		return err
	}
	return printNewestINBOX(ctx, client)
}

// connectWithPin connects. If the certificate is not pinned and trust is set,
// it pins the presented fingerprint in config.json and connects again.
func connectWithPin(ctx context.Context, mgr *config.Manager, cfg config.Config, idx int, password string, trust bool) (mail.Client, error) {
	acct := cfg.Bridge[idx]
	client := mail.NewClient()
	err := client.Connect(ctx, acct, password)

	var untrusted *mail.UntrustedCertError
	if !errors.As(err, &untrusted) {
		if err != nil {
			_ = client.Close()
			return nil, err
		}
		return client, nil
	}

	fmt.Printf("Bridge certificate is not pinned yet.\n  SHA-256: %s\n  Subject: %s\n",
		untrusted.Fingerprint, untrusted.Subject)
	fmt.Println("Compare the SHA-256 with the value shown in Proton Bridge before trusting it.")
	if !trust {
		_ = client.Close()
		return nil, errors.New("certificate not trusted; re-run with -trust after checking the fingerprint")
	}

	cfg.Bridge[idx].CertFingerprint = untrusted.Fingerprint
	if err := mgr.Save(cfg); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("save pinned fingerprint: %w", err)
	}
	fmt.Printf("Pinned in %s. Reconnecting.\n", mgr.Path())

	_ = client.Close()
	client = mail.NewClient()
	if err := client.Connect(ctx, cfg.Bridge[idx], password); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

func printMailboxes(ctx context.Context, client mail.Client) error {
	boxes, err := client.ListMailboxes(ctx)
	if err != nil {
		return fmt.Errorf("list mailboxes: %w", err)
	}
	sort.Slice(boxes, func(i, j int) bool { return boxes[i].Name < boxes[j].Name })
	fmt.Printf("%-36s %-8s %9s %7s\n", "MAILBOX", "KIND", "MESSAGES", "UNSEEN")
	for _, b := range boxes {
		fmt.Printf("%-36s %-8s %9d %7d\n", b.Name, b.Kind, b.Messages, b.Unseen)
	}
	return nil
}

// printNewestINBOX prints the five newest INBOX subjects. "Newest" means the
// highest UIDs, looked at within the last 500 UIDs.
func printNewestINBOX(ctx context.Context, client mail.Client) error {
	info, err := client.MailboxInfo(ctx, "INBOX")
	if err != nil {
		return fmt.Errorf("inspect INBOX: %w", err)
	}
	fmt.Printf("\nINBOX: UIDVALIDITY %d, highest UID %d, %d messages\n",
		info.UIDValidity, info.HighestUID, info.Messages)
	if info.HighestUID == 0 {
		return nil
	}
	first := uint32(1)
	if info.HighestUID > 500 {
		first = info.HighestUID - 499
	}
	headers, err := client.FetchHeaders(ctx, "INBOX", mail.UIDRange{First: first})
	if err != nil {
		return fmt.Errorf("fetch INBOX headers: %w", err)
	}
	fmt.Println("Newest INBOX subjects (by UID):")
	for i := len(headers) - 1; i >= 0 && i >= len(headers)-5; i-- {
		h := headers[i]
		from := ""
		if len(h.From) > 0 {
			from = h.From[0].Email
		}
		// %q keeps control characters from a hostile subject out of the terminal.
		fmt.Printf("  %6d  %s  %s  %q\n", h.UID, h.Date.Format("2006-01-02 15:04"), from, h.Subject)
	}
	return nil
}
