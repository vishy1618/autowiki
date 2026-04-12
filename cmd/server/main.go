package main

import (
	"flag"
	"log"
	"os"

	"github.com/joho/godotenv"
	"github.com/suvish/autowiki/internal/config"
	"github.com/suvish/autowiki/internal/server"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	dev := flag.Bool("dev", false, "proxy non-API traffic to Remix dev server")
	flag.Parse()

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

	srv := server.New(cfg, *dev)
	if err := srv.Start(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
