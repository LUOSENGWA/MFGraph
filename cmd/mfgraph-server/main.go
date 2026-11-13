// Command mfgraph-server runs the MFGraph HTTP service.
//
//	mfgraph-server [serve]   start the HTTP server (default)
//	mfgraph-server migrate   apply pending database migrations and exit
//	mfgraph-server version   print the version
//
// Configuration comes from MFGRAPH_* environment variables; see
// internal/config.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql" // database/sql driver "mysql"

	"github.com/LUOSENGWA/MFGraph/internal/api"
	"github.com/LUOSENGWA/MFGraph/internal/config"
	"github.com/LUOSENGWA/MFGraph/internal/ids"
	"github.com/LUOSENGWA/MFGraph/internal/migrate"
	"github.com/LUOSENGWA/MFGraph/migrations"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "version", "--version":
		fmt.Fprintf(stdout, "mfgraph-server %s\n", version)
		return 0
	case "help", "-h", "--help":
		fmt.Fprintln(stdout, "Usage: mfgraph-server [serve|migrate|version]")
		return 0
	case "serve", "migrate":
	default:
		fmt.Fprintf(stderr, "mfgraph-server: unknown command %q\n", cmd)
		return 2
	}
	cfg, err := config.Load(getenv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	logger := newLogger(cfg, stderr)
	if cmd == "migrate" {
		if err := migrateOnly(ctx, cfg, logger); err != nil {
			logger.Error("migration failed", "err", err)
			return 1
		}
		return 0
	}
	ln, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		logger.Error("listen", "addr", cfg.HTTPAddr, "err", err)
		return 1
	}
	if err := serve(ctx, cfg, ln, logger); err != nil {
		logger.Error("server stopped", "err", err)
		return 1
	}
	return 0
}

func newLogger(cfg config.Config, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

func openDB(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return db, nil
}

func migrationPlans(cfg config.Config) []migrate.Plan {
	var plans []migrate.Plan
	if cfg.KernelMigrations {
		plans = append(plans, migrate.Plan{Component: migrate.ComponentKernel, Source: migrations.Kernel()})
	}
	return append(plans, migrate.Plan{Component: migrate.ComponentMFGraph, Source: migrations.MySQL()})
}

func runMigrations(ctx context.Context, cfg config.Config, db *sql.DB, logger *slog.Logger) error {
	results, err := migrate.RunAll(ctx, migrate.SQLTarget{DB: db}, migrationPlans(cfg), time.Now)
	for i, r := range results {
		logger.Info("migrations", "component", migrationPlans(cfg)[i].Component, "applied", r.Applied, "skipped", len(r.Skipped))
	}
	return err
}

func migrateOnly(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	if cfg.DBDSN == "" {
		return errors.New(config.EnvDBDSN + " is required")
	}
	db, err := openDB(ctx, cfg.DBDSN)
	if err != nil {
		return err
	}
	defer db.Close()
	return runMigrations(ctx, cfg, db, logger)
}

// serve runs the HTTP server on ln until ctx is canceled, then shuts down
// gracefully.
func serve(ctx context.Context, cfg config.Config, ln net.Listener, logger *slog.Logger) error {
	logger.Info("starting mfgraph-server", append([]any{"version", version}, cfg.Summary()...)...)
	dbCheck := api.ReadinessCheck{Name: "db", Code: api.CodeDBUnavailable}
	if cfg.DBDSN != "" {
		db, err := openDB(ctx, cfg.DBDSN)
		if err != nil {
			return err
		}
		defer db.Close()
		if cfg.MigrateOnStart {
			if err := runMigrations(ctx, cfg, db, logger); err != nil {
				return err
			}
		}
		dbCheck.Check = db.PingContext
	}
	authn, err := api.NewStaticTokens(cfg.AuthTokens)
	if err != nil {
		return err
	}
	if len(cfg.AuthTokens) == 0 {
		logger.Warn("no " + config.EnvAuthTokens + " configured: every authenticated endpoint answers 401")
	}
	srv := &http.Server{
		Handler: api.NewHandler(api.Options{
			Authenticator: authn,
			IDGen:         ids.NewGenerator(),
			Readiness:     []api.ReadinessCheck{dbCheck},
			Logger:        logger,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	logger.Info("listening", "addr", ln.Addr().String())
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	logger.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
