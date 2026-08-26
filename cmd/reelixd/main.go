// Command reelixd is the Reelix media server.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/maverickman79/reelix/internal/config"
	"github.com/maverickman79/reelix/internal/db"
	"github.com/maverickman79/reelix/internal/logging"
	"github.com/maverickman79/reelix/internal/server"
)

// version is stamped at build time:
//
//	go build -ldflags "-X main.version=0.0.1"
//
// The fallback keeps a plain `go build` or `go run` honest about what it is.
var version = "0.0.1-dev"

func main() {
	if err := run(); err != nil {
		// The logger may not exist yet when configuration fails, so the
		// startup path reports to stderr directly.
		fmt.Fprintf(os.Stderr, "reelixd: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logging.New(os.Stdout, cfg.Log.Format, cfg.Log.Level)
	slog.SetDefault(log)

	log.Info("reelix starting",
		slog.String(logging.KeyComponent, "main"),
		slog.String("version", version),
		slog.String("addr", cfg.HTTP.Addr),
		slog.String("config_dir", cfg.Paths.ConfigDir),
		slog.String("cache_dir", cfg.Paths.CacheDir),
		// Redacted, never the DSN: it carries the database password.
		slog.String("database", cfg.Database.Redacted()))

	// SIGINT and SIGTERM begin a graceful shutdown; a second signal restores
	// the default behaviour so an operator can always force an exit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.Database, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Migrations run before the listener binds. Docker-first deployment means
	// there is no separate migrate step an operator can be relied on to run,
	// and serving requests against a half-migrated schema is worse than
	// failing to start.
	if err := db.Migrate(ctx, pool, log); err != nil {
		return fmt.Errorf("migrating database: %w", err)
	}

	srv := server.New(cfg.HTTP, log, version)
	if err := srv.Run(ctx); err != nil {
		return fmt.Errorf("http server: %w", err)
	}

	log.Info("reelix stopped", slog.String(logging.KeyComponent, "main"))
	return nil
}
