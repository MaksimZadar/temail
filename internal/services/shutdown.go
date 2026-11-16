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
//  3. Close the action service: refuse new calls, wait for in-flight writes, close
//     its Writer connections and the audit log (its verified writes go to the
//     cache).
//  4. Close the cache, but only if all steps finished. If a run, a read or a
//     write is still going, the cache stays open and the error says so, rather
//     than letting a write hit a closed database.
func Shutdown(syncSvc *SyncService, mailSvc *MailService, st store.Store, actionSvcs ...*ActionService) error {
	syncErr := syncSvc.Stop()
	mailErr := mailSvc.shutdown()
	var actionErr error
	for _, a := range actionSvcs {
		if a != nil {
			actionErr = errors.Join(actionErr, a.shutdown())
		}
	}
	if err := errors.Join(syncErr, mailErr, actionErr); err != nil {
		log.Printf("temail: not closing the cache: %v", err)
		return fmt.Errorf("cache left open: %w", err)
	}
	return st.Close()
}
