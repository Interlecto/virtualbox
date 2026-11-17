// Command virtualbox is the virtual mail server daemon.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/interlecto/virtualbox/internal/config"
	"github.com/interlecto/virtualbox/internal/policy"
	"github.com/interlecto/virtualbox/internal/queue"
	"github.com/interlecto/virtualbox/internal/smtpd"
	"github.com/interlecto/virtualbox/internal/store"
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

	if err := run(ctx, cfg, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	log.Info("virtualbox starting", "hostname", cfg.Hostname, "domains", len(cfg.Domains))

	q, err := queue.OpenFileQueue(filepath.Join(cfg.DataDir, "queue"), queue.FileOptions{})
	if err != nil {
		return err
	}

	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	db, err := store.Open(cfg.DatabasePath())
	if err != nil {
		return err
	}
	defer db.Close()
	seeded, err := db.SeedFromConfig(ctx, cfg)
	if err != nil {
		return err
	}
	log.Info("configuration store ready", "path", cfg.DatabasePath(), "seeded_rules", seeded)

	tlsCfg, err := loadTLS(cfg.TLS)
	if err != nil {
		return err
	}

	var servers []*smtpd.MX

	if cfg.Listen.MX != "" {
		mx, err := smtpd.NewMX(smtpd.Config{
			Hostname:            cfg.Hostname,
			Addr:                cfg.Listen.MX,
			TLS:                 tlsCfg,
			Resolver:            policy.NewAliasResolver(db),
			Queue:               q,
			Logger:              log,
			MaxMessageBytes:     cfg.MX.MaxMessageBytes,
			MaxRecipients:       cfg.MX.MaxRecipients,
			MaxConnections:      cfg.MX.MaxConnections,
			MaxConnectionsPerIP: cfg.MX.MaxConnectionsPerIP,
			Timeout:             time.Duration(cfg.MX.TimeoutSeconds) * time.Second,
		})
		if err != nil {
			return err
		}
		if err := mx.Listen(); err != nil {
			return err
		}
		servers = append(servers, mx)
		go func() {
			if err := mx.Serve(); err != nil {
				log.Error("mx stopped", "err", err)
			}
		}()
	}

	// TODO(roadmap 1.4): start the dispatcher. Until then accepted mail
	// stays in the queue and is not delivered anywhere.
	log.Warn("delivery is not implemented yet: accepted mail remains queued (see ROADMAP.md)")

	<-ctx.Done()
	log.Info("virtualbox stopping")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, s := range servers {
		if err := s.Shutdown(shutdownCtx); err != nil {
			log.Warn("shutdown", "err", err)
		}
	}
	return nil
}

// loadTLS returns the server TLS configuration, or nil when TLS is not
// configured (STARTTLS is then not offered).
func loadTLS(c config.TLS) (*tls.Config, error) {
	if c.CertFile == "" {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load tls certificate: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}
