// Command virtualbox is the virtual mail server daemon.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"

	"github.com/interlecto/virtualbox/internal/config"
)

func main() {
	cfgPath := flag.String("config", "config.json", "path to the JSON configuration file")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Error("cannot load configuration", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	log.Info("virtualbox starting",
		"hostname", cfg.Hostname,
		"domains", len(cfg.Domains))

	// TODO(roadmap phase 1): start MX listener, queue and dispatcher here.
	log.Warn("no services implemented yet; see ROADMAP.md")

	<-ctx.Done()
	log.Info("virtualbox stopping")
}
