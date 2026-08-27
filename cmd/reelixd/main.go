// Command reelixd is the Reelix media server.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	v1 "github.com/maverickman79/reelix/internal/api/v1"
	"github.com/maverickman79/reelix/internal/compat/jellyfin"
	"github.com/maverickman79/reelix/internal/config"
	"github.com/maverickman79/reelix/internal/db"
	"github.com/maverickman79/reelix/internal/logging"
	"github.com/maverickman79/reelix/internal/media"
	"github.com/maverickman79/reelix/internal/server"
	"github.com/maverickman79/reelix/internal/service"
)

// version is stamped at build time:
//
//	go build -ldflags "-X main.version=0.0.1"
//
// The fallback keeps a plain `go build` or `go run` honest about what it is.
var version = "0.0.1-dev"

func main() {
	// run reports its own failures: to stderr while there is no logger, and
	// through the logger once there is one. Printing here as well would
	// duplicate the second case and emit it in the wrong format.
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		// The logger cannot be built until the configuration that describes it
		// parses, so this one failure goes to stderr. It is also the failure
		// most likely to be read by a human running the container by hand.
		fmt.Fprintf(os.Stderr, "reelixd: %v\n", err)
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

	// fail records a startup failure through the logger. Everything past this
	// point has one, so a crash should be as greppable as a normal event
	// rather than an unstructured line on stderr.
	fail := func(operation string, err error) error {
		log.Error("startup failed",
			slog.String(logging.KeyComponent, "main"),
			slog.String(logging.KeyOperation, operation),
			slog.String(logging.KeyError, err.Error()))
		return err
	}

	pool, err := db.Open(ctx, cfg.Database, log)
	if err != nil {
		return fail("db_open", err)
	}
	defer pool.Close()

	// Migrations run before the listener binds. Docker-first deployment means
	// there is no separate migrate step an operator can be relied on to run,
	// and serving requests against a half-migrated schema is worse than
	// failing to start.
	if err := db.Migrate(ctx, pool, log); err != nil {
		return fail("migrate", fmt.Errorf("migrating database: %w", err))
	}

	// Confirm ffprobe exists and runs before serving. A missing binary should
	// be a startup failure naming the path, not a scan that fails minutes
	// later on its first file.
	prober := media.NewProber(cfg.Media.FFprobePath, cfg.Media.ProbeTimeout)

	probeVersion, err := prober.Version(ctx)
	if err != nil {
		return fail("ffprobe", fmt.Errorf("checking %s: %w", cfg.Media.FFprobePath, err))
	}
	log.Info("media tools ready",
		slog.String(logging.KeyComponent, "main"),
		slog.String("ffprobe", cfg.Media.FFprobePath),
		slog.String("version", probeVersion))

	scans := service.NewScanService(pool, prober, log)

	// Jobs run in-process, so anything still marked running belongs to a
	// process that no longer exists.
	if err := scans.ReapOrphanedJobs(ctx); err != nil {
		return fail("reap_jobs", err)
	}

	sessions := service.NewSessionService(pool)

	nativeAPI := v1.New(
		service.NewAuthService(pool),
		service.NewLibraryService(pool),
		scans,
	)

	compatAPI := jellyfin.New(sessions, log)

	srv := server.New(cfg.HTTP, log, version, nativeAPI, compatAPI)
	if err := srv.Run(ctx); err != nil {
		return fail("serve", fmt.Errorf("http server: %w", err))
	}

	log.Info("reelix stopped", slog.String(logging.KeyComponent, "main"))
	return nil
}
