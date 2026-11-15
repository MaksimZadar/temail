package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/secrets"
	"github.com/MaksimZadar/temail/internal/services"
	"github.com/MaksimZadar/temail/internal/store"
)

// Wails embeds the built frontend (frontend/dist) into the binary.
//
//go:embed all:frontend/dist
var assets embed.FS

// main wires the app: config, keyring, cache, the Wails services and one
// window. Every bound method is callable by the webview; see
// internal/services and docs/ARCHITECTURE.md §3.3.
func main() {
	cfg, err := config.DefaultManager()
	if err != nil {
		log.Fatal(err)
	}
	dbPath, err := store.DefaultPath()
	if err != nil {
		log.Fatal(err)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("open cache: %v", err)
	}
	keyring := secrets.NewKeyringStore()

	app := application.New(application.Options{
		Name:        "temail",
		Description: "Proton Mail desktop client with LLM assistance",
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
	})

	// Shutdown order: this hook runs first (stop sync, close the cache), then
	// the services shut down (the Bridge session is closed by MailService).
	syncSvc := services.NewSyncService(st, cfg, keyring, mail.NewClient, app.Event)
	mailSvc := services.NewMailService(st, cfg, keyring, mail.NewClient)
	setupSvc := services.NewSetupService(cfg, keyring, mail.NewClient, syncSvc)
	app.RegisterService(application.NewService(syncSvc))
	app.RegisterService(application.NewService(mailSvc))
	app.RegisterService(application.NewService(setupSvc))

	app.OnShutdown(func() {
		if err := syncSvc.Stop(); err != nil {
			log.Printf("temail: stop sync: %v", err)
		}
		if err := st.Close(); err != nil {
			log.Printf("temail: close cache: %v", err)
		}
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "temail",
		Width:            1280,
		Height:           800,
		BackgroundColour: application.NewRGB(255, 255, 255),
		URL:              "/",
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
