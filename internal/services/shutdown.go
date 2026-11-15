package services

import (
	"errors"
	"fmt"
	"log"

	"github.com/MaksimZadar/temail/internal/store"
)

// Shutdown tears the services down in dependency order. It is called from the
// app's shutdown hook, and it is not a bound method.
//
//  1. Stop the sync. Its writes go to the cache.
//  2. Close the mail service: refuse new calls, cancel running ones, close the
//     Bridge session and wait for in-flight calls (their writes go to the cache).
//  3. Close the cache, but only if both steps finished. If a run or a read is
//     still going, the cache stays open and the error says so, rather than
//     letting a write hit a closed database.
func Shutdown(syncSvc *SyncService, mailSvc *MailService, st store.Store) error {
	syncErr := syncSvc.Stop()
	mailErr := mailSvc.shutdown()
	if err := errors.Join(syncErr, mailErr); err != nil {
		log.Printf("temail: not closing the cache: %v", err)
		return fmt.Errorf("cache left open: %w", err)
	}
	return st.Close()
}
