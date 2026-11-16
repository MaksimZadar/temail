package main

import (
	"embed"
	"log"
	"log/slog"

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
		// Wails logs bound-call arguments and results at Debug, which would
		// include the Bridge password and message bodies. Never Debug.
		LogLevel: slog.LevelInfo,
	})

	syncSvc := services.NewSyncService(st, cfg, keyring, mail.NewClient, app.Event)
	mailSvc := services.NewMailService(st, cfg, keyring, mail.NewClient)
	setupSvc := services.NewSetupService(cfg, keyring, mail.NewClient, syncSvc)
	actionSvc := services.NewActionService(st, cfg, keyring, mail.NewWriter, syncSvc, app.Event)
	llmSvc := services.NewLLMService(cfg, keyring, services.NewNativeConfirmer(app.Dialog))
	suggestSvc := services.NewSuggestionService(st, cfg, llmSvc, actionSvc, app.Event)
	app.RegisterService(application.NewService(syncSvc))
	app.RegisterService(application.NewService(mailSvc))
	app.RegisterService(application.NewService(setupSvc))
	app.RegisterService(application.NewService(actionSvc))
	app.RegisterService(application.NewService(suggestSvc))
	app.RegisterService(application.NewService(llmSvc))

	// The ordered teardown (sync, mail reads, action writes, then the cache)
	// runs in this hook, before the services' own shutdown hooks. It leaves the
	// cache open if a run, a read or a write did not finish in time.
	app.OnShutdown(func() {
		if err := services.Shutdown(syncSvc, mailSvc, st, suggestSvc, actionSvc); err != nil {
			log.Printf("temail: shutdown incomplete: %v", err)
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
