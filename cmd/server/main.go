package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"os"

	"github.com/joho/godotenv"
	"github.com/suvish/autowiki/internal/config"
	"github.com/suvish/autowiki/internal/dream"
	"github.com/suvish/autowiki/internal/drivesync"
	"github.com/suvish/autowiki/internal/llm"
	"github.com/suvish/autowiki/internal/server"
	"github.com/suvish/autowiki/internal/store"
	"github.com/suvish/autowiki/internal/vault"
)


func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	dev := flag.Bool("dev", false, "proxy non-API traffic to Remix dev server")
	debug := flag.Bool("debug", false, "enable debug-level logging")
	flag.Parse()

	if *debug {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}

	// Load .env if present. Existing shell env vars take precedence.
	if _, err := os.Stat(".env"); err == nil {
		if err := godotenv.Load(); err != nil {
			log.Fatalf("failed to load .env: %v", err)
		}
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	db, err := store.OpenPebble(cfg.PebblePath)
	if err != nil {
		log.Fatalf("failed to open pebble store: %v", err)
	}
	defer db.Close()

	sessions := store.NewPebbleStore(db)
	chats := store.NewPebbleChatStore(db)
	// Chat and attachment description use a fast model (default: Haiku).
	haikuClient := llm.NewClient(llm.Config{
		APIKey: cfg.AnthropicAPIKey,
		Model:  cfg.ChatModel,
	})
	// Dream consolidation uses a capable model (default: Sonnet).
	sonnetClient := llm.NewClient(llm.Config{
		APIKey: cfg.AnthropicAPIKey,
		Model:  cfg.DreamModel,
	})
	vm := vault.NewManager(cfg.VaultPath)

	// Start dream runner as a background goroutine.
	dreamCtx, dreamCancel := context.WithCancel(context.Background())
	defer dreamCancel()
	consolidateFn := func(ctx context.Context) error {
		return dream.Consolidate(ctx, vm, sonnetClient)
	}
	dreamer := dream.NewRunner(vm, consolidateFn, cfg.Dream.StartHourUTC, cfg.Dream.EndHourUTC)
	go dreamer.Start(dreamCtx)

	var driveTokenStore store.DriveTokenStore
	if cfg.DriveSync.Enabled {
		driveTokenStore = sessions
	}

	if cfg.DriveSync.Enabled {
		syncCtx, syncCancel := context.WithCancel(context.Background())
		defer syncCancel()
		sm := drivesync.New(cfg.DriveSync, cfg.Auth.GoogleClientID, cfg.Auth.GoogleClientSecret, sessions)
		go sm.Start(syncCtx)
	}

	srv := server.New(cfg, sessions, chats, haikuClient, vm, haikuClient, consolidateFn, driveTokenStore, *dev)
	if err := srv.Start(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
