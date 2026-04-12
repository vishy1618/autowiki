package main

import (
	"flag"
	"log"

	"github.com/suvish/autowiki/internal/config"
	"github.com/suvish/autowiki/internal/server"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	dev := flag.Bool("dev", false, "proxy non-API traffic to Remix dev server")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	srv := server.New(cfg, *dev)
	if err := srv.Start(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
