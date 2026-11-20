// Command mfgraph-server runs the MFGraph HTTP service.
//
//	mfgraph-server [options] [serve]   start the HTTP server (default)
//	mfgraph-server [options] migrate   apply pending database migrations and exit
//	mfgraph-server version             print the version
//
// Options, before or after the command:
//
//	--config FILE   read settings from FILE (KEY=VALUE lines, see
//	                config.ParseFile); a relative path is relative to the
//	                working directory
//	--socket PATH   listen on the Unix domain socket PATH
//
// Settings come from MFGRAPH_* (and, for a module process, MODULE_*)
// environment variables; see internal/config. Command-line options win over
// the environment, which wins over the configuration file, which wins over
// the defaults. The service needs a database (MFGRAPH_DB_DSN) and a storage
// root (MFGRAPH_STORAGE_ROOT): in standalone mode it starts without a
// database, but /readyz and every business endpoint answer 503
// db_unavailable. It listens on host:port or, with
// MFGRAPH_HTTP_ADDR=unix:<path>, on a Unix domain socket (mode 0600); module
// mode (MFGRAPH_MODE=module) listens only on a socket.
//
// Before it serves or migrates, the server checks that the Unicode tables
// compiled into it match the specification (pathx.CheckUnicodeVersions): the
// standard library's tables must be exactly V and those of golang.org/x/text
// at least V. Otherwise it refuses to start; there is no switch to skip this.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql" // also the database/sql driver "mysql"

	"github.com/LUOSENGWA/MFGraph/internal/actorref"
	"github.com/LUOSENGWA/MFGraph/internal/api"
	"github.com/LUOSENGWA/MFGraph/internal/authz"
	"github.com/LUOSENGWA/MFGraph/internal/config"
	"github.com/LUOSENGWA/MFGraph/internal/core"
	"github.com/LUOSENGWA/MFGraph/internal/ids"
	"github.com/LUOSENGWA/MFGraph/internal/migrate"
	"github.com/LUOSENGWA/MFGraph/internal/outbox"
	"github.com/LUOSENGWA/MFGraph/internal/pathx"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/store/mysql"
	"github.com/LUOSENGWA/MFGraph/internal/treestore"
	"github.com/LUOSENGWA/MFGraph/migrations"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

// unicodeTables reports the Unicode versions of the tables of this build.
// Only tests replace it, to simulate other toolchains.
var unicodeTables = pathx.TableVersions

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

const usage = `Usage: mfgraph-server [--config FILE] [--socket PATH] [serve|migrate|version|help]
The options may also follow the command.`

// invocation is a parsed command line.
type invocation struct {
	cmd    string // serve (default), migrate, version or help
	config string // --config
	socket string // --socket
}

// parseArgs parses the command and the options --config and --socket
// ("--opt value" or "--opt=value"), which may come before or after the
// command; each may be given once.
func parseArgs(args []string) (invocation, error) {
	var inv invocation
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if name, value, hasValue := strings.Cut(a, "="); name == "--config" || name == "--socket" {
			if seen[name] {
				return inv, fmt.Errorf("%s is given twice", name)
			}
			seen[name] = true
			if !hasValue {
				if i+1 == len(args) {
					return inv, fmt.Errorf("%s needs a value", name)
				}
				i++
				value = args[i]
			}
			if value == "" {
				return inv, fmt.Errorf("%s needs a value", name)
			}
			if name == "--config" {
				inv.config = value
			} else {
				inv.socket = value
			}
			continue
		}
		switch a {
		case "--version":
			a = "version"
		case "-h", "--help":
			a = "help"
		}
		switch {
		case strings.HasPrefix(a, "-"):
			return inv, fmt.Errorf("unknown option %q", a)
		case inv.cmd != "":
			return inv, fmt.Errorf("unexpected argument %q after the command %s", a, inv.cmd)
		case a == "serve" || a == "migrate" || a == "version" || a == "help":
			inv.cmd = a
		default:
			return inv, fmt.Errorf("unknown command %q", a)
		}
	}
	if inv.cmd == "" {
		inv.cmd = "serve"
	}
	return inv, nil
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	inv, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "mfgraph-server: %v\n%s\n", err, usage)
		return 2
	}
	cmd := inv.cmd
	switch cmd {
	case "version":
		fmt.Fprintf(stdout, "mfgraph-server %s\n", version)
		return 0
	case "help":
		fmt.Fprintln(stdout, usage)
		return 0
	}
	lookup := getenv
	if inv.config != "" {
		file, err := config.ReadFile(inv.config)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		lookup = config.Layered(getenv, file)
	}
	cfg, err := config.Load(lookup)
	if err == nil && inv.socket != "" {
		err = cfg.SetSocket(inv.socket)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	logger := newLogger(cfg, stderr)
	if !unicodeTablesMatch(logger) {
		return 1
	}
	for _, w := range cfg.Warnings() {
		logger.Warn(w)
	}
	if cmd == "serve" {
		if err := cfg.CheckServe(); err != nil {
			logger.Error("refusing to start", "err", err.Error())
			return 2
		}
	}
	if cmd == "migrate" {
		if err := migrateOnly(ctx, cfg, logger); err != nil {
			logger.Error("migration failed", "err", err)
			return 1
		}
		return 0
	}
	ln, err := listen(cfg)
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

// unicodeTablesMatch logs and reports whether the Unicode tables of this
// build may run the write-time rules of V (pathx.CheckUnicodeVersions).
func unicodeTablesMatch(logger *slog.Logger) bool {
	std, xnorm, xcases := unicodeTables()
	if err := pathx.CheckUnicodeVersions(std, xnorm, xcases); err != nil {
		logger.Error("refusing to start: the Unicode tables of this build do not match the specification",
			"required_v", pathx.UnicodeVersion, "unicode", std, "x_text_norm", xnorm, "x_text_cases", xcases,
			"err", err.Error())
		return false
	}
	return true
}

func newLogger(cfg config.Config, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

// listen opens the TCP or Unix domain socket listener of cfg.HTTPAddr.
func listen(cfg config.Config) (net.Listener, error) {
	if path, ok := cfg.UnixSocket(); ok {
		return listenUnix(path)
	}
	return net.Listen("tcp", cfg.HTTPAddr)
}

// listenUnix listens on a Unix domain socket that only the server's user
// can use (mode 0600). A socket left at path by a server that is gone is
// removed; anything else at path (a file, a directory, or a socket a
// running server answers on) stops the start.
func listenUnix(path string) (net.Listener, error) {
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket; refusing to replace it", path)
		}
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			c.Close()
			return nil, fmt.Errorf("%s is the socket of a running server", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove the stale socket %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := listenUnixPrivate(path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// openDB opens and pings the database of dsn, which config.Load has parsed
// already; an error never repeats the DSN.
func openDB(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, errors.New("the database DSN is not valid")
	}
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(32)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)
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

// pseudonymizer returns the log pseudonymizer: with the configured key, or
// with a random one (logged as a warning: pseudonyms then change with every
// start).
func pseudonymizer(cfg config.Config, logger *slog.Logger) (*actorref.Pseudonymizer, error) {
	key := []byte(cfg.LogPseudonymKey)
	if len(key) == 0 {
		logger.Warn("no " + config.EnvLogPseudonymKey + " configured: a random key is used, so the actor pseudonyms " +
			"in the logs cannot be linked across restarts")
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
	}
	return actorref.NewPseudonymizer(key)
}

// authenticator returns the static bearer tokens of standalone mode or the
// identity headers of module mode.
func authenticator(cfg config.Config, logger *slog.Logger) (ports.Authenticator, error) {
	if cfg.Module() {
		return api.NewTrustedHeaders(cfg.TrustedHeaderPrefix, cfg.InternalTokenHeader, cfg.InternalToken)
	}
	if len(cfg.AuthTokens) == 0 {
		logger.Warn("no " + config.EnvAuthTokens + " configured: every authenticated endpoint answers 401")
	}
	return api.NewStaticTokens(cfg.AuthTokens)
}

// serve runs the HTTP server on ln until ctx is canceled, then shuts down
// gracefully within cfg.ShutdownTimeout.
func serve(ctx context.Context, cfg config.Config, ln net.Listener, logger *slog.Logger) error {
	defer ln.Close()
	logger.Info("starting mfgraph-server", append([]any{"version", version, "unicode_version", pathx.UnicodeVersion}, cfg.Summary()...)...)
	dbCheck := api.ReadinessCheck{Name: "db", Code: api.CodeDBUnavailable, Required: true}
	storageCheck := api.ReadinessCheck{Name: "storage", Code: api.CodeStorageUnavailable}
	readiness := []api.ReadinessCheck{dbCheck, storageCheck}
	var svc *core.Service
	var fwd *outbox.Forwarder
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
		objects, backendID, err := openStorage(cfg, logger)
		if err != nil {
			return err
		}
		store := mysql.New(db)
		prefixes := treestore.CachedPrefixes(func(ctx context.Context, ws string) (string, error) {
			w, err := store.GetWorkspace(ctx, nil, ws)
			return w.StoragePrefix, err
		}, 10_000)
		trees, err := treestore.New(objects, prefixes)
		if err != nil {
			return err
		}
		deps := core.Deps{Store: store, Objects: objects, Trees: trees, Authz: authz.ProjectOwner{}, Clock: ports.ClockFunc(time.Now),
			IDs: ids.NewGenerator(), Logger: logger}
		schema := newSchemaCheck(cfg, db, logger)
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := schema.check(cctx); err != nil && !errors.As(err, new(*api.CheckError)) {
			logger.Warn("cannot read the recorded migrations", "err", err.Error())
		}
		cancel()
		readiness[0].Check = func(ctx context.Context) error {
			if err := store.Ping(ctx); err != nil {
				return err
			}
			return schema.check(ctx)
		}
		readiness[1].Check = objects.Check
		if cfg.Module() {
			deps.Authz = authz.ProjectScope{}
			ops, sink, err := hostOutbox(cfg)
			if err != nil {
				return err
			}
			defer ops.Close()
			fwd = outbox.New(store, sink, outbox.Options{Logger: logger})
			deps.EventsCommitted = fwd.Notify
			// The unique index on event_id, logged at the start (and by
			// /readyz), without delaying it; the table may not be reachable
			// yet.
			go func() {
				cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				if err := fwd.CheckSink(cctx); err != nil {
					logger.Warn("outbox: the host's outbox table cannot be reached yet; the forwarder retries", "err", err.Error())
				}
			}()
			// Ready only when the host's outbox table can be written.
			readiness = append(readiness, api.ReadinessCheck{Name: "outbox", Code: api.CodeDBUnavailable, Required: true,
				Check: fwd.CheckSink})
		}
		svc, err = core.New(core.Config{TenantID: cfg.TenantID, StorageBackendID: backendID, PackSizes: cfg.PackSizes, MaxUploadBytes: cfg.MaxUploadBytes,
			MaxCommitChanges: cfg.MaxCommitChanges, IdempotencyLease: cfg.IdempotencyLease, HostOwnsProjects: cfg.Module()}, deps)
		if err != nil {
			return err
		}
	} else {
		logger.Warn("no " + config.EnvDBDSN + " configured: /readyz and every business endpoint answer 503 db_unavailable")
	}
	authn, err := authenticator(cfg, logger)
	if err != nil {
		return err
	}
	pseudo, err := pseudonymizer(cfg, logger)
	if err != nil {
		return err
	}
	opts := api.Options{
		Authenticator: authn,
		IDGen:         ids.NewGenerator(),
		Readiness:     readiness,
		Logger:        logger,
		Service:       svc,
		APIPrefix:     cfg.APIPrefix,
		Version:       version,
		Pseudonym:     pseudo.Pseudonym,
		// The upload limit is MaxUploadBytes (core).
		MaxJSONBodyBytes: cfg.MaxJSONBodyBytes,
	}
	if cfg.Module() {
		opts.TrustedHeaderPrefix, opts.NoProjects = cfg.TrustedHeaderPrefix, true
	}
	if fwd != nil {
		opts.Outbox = func(ctx context.Context) (api.OutboxState, error) {
			st, err := fwd.Status(ctx)
			return api.OutboxState{Pending: st.Pending, OldestPendingAt: st.OldestPendingAt, Stalled: st.Stalled,
				StalledSince: st.StalledSince, LastError: st.LastError}, err
		}
	}
	srv := &http.Server{
		Handler:           api.NewHandler(opts),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	// The forwarder of module mode runs until the server stops.
	fctx, stopForwarder := context.WithCancel(context.Background())
	defer stopForwarder()
	forwarderDone := make(chan struct{})
	if fwd != nil {
		go func() { fwd.Run(fctx); close(forwarderDone) }()
	} else {
		close(forwarderDone)
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	logger.Info("listening", "addr", ln.Addr().String(), "api_prefix", cfg.APIPrefix)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	// Shutdown: stop accepting requests and finish the running ones, then
	// deliver what the forwarder can in the time that is left; all of it
	// within the shutdown budget (module mode: 1 s less than
	// ShutdownTimeout, which the host's 8 s caps). Events not delivered
	// stay in the local outbox and go out after the next start. The
	// database calls of the forwarder end when their context does, so an
	// unresponsive database does not hold the process.
	budget := cfg.ShutdownBudget()
	logger.Info("shutting down", "timeout", cfg.ShutdownTimeout.String(), "budget", budget.String())
	sctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	shutdownErr := srv.Shutdown(sctx)
	stopForwarder()
	select {
	case <-forwarderDone:
		if fwd != nil {
			n, err := fwd.Flush(sctx)
			attrs := []any{"delivered", n}
			if err != nil {
				attrs = append(attrs, "err", err.Error())
			}
			logger.Info("outbox: last delivery before the stop", attrs...)
		}
	case <-sctx.Done():
		logger.Warn("outbox: the forwarder did not stop within the shutdown budget; the pending events go out after the next start")
	}
	if shutdownErr != nil {
		return shutdownErr
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// schemaCheck compares the migrations this binary embeds with those the
// database recorded. Missing ones (a binary newer than its schema, for
// example after an upgrade without the migrate command) make /readyz
// answer 503 db_unavailable with details.reason schema_behind and the list
// in details.missing, and are logged as an ERROR at the start and at most
// every 5 minutes after. Recorded versions this binary does not know (an
// older binary on a newer schema) are fine.
type schemaCheck struct {
	plans  []migrate.Plan
	target migrate.Target
	logger *slog.Logger
	mu     sync.Mutex
	logged time.Time
	last   string // the missing migrations last logged
}

const schemaLogEvery = 5 * time.Minute

func newSchemaCheck(cfg config.Config, db *sql.DB, logger *slog.Logger) *schemaCheck {
	return &schemaCheck{plans: migrationPlans(cfg), target: migrate.SQLTarget{DB: db}, logger: logger}
}

func (s *schemaCheck) check(ctx context.Context) error {
	missing, err := migrate.Missing(ctx, s.target, s.plans)
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}
	list := strings.Join(missing, ", ")
	s.mu.Lock()
	log := list != s.last || time.Since(s.logged) >= schemaLogEvery
	if log {
		s.logged, s.last = time.Now(), list
	}
	s.mu.Unlock()
	if log {
		s.logger.Error("the database schema is behind this build: run the migrate command (mfgraph-server migrate) before "+
			"starting it; until then requests can fail", "missing_migrations", list)
	}
	return &api.CheckError{Reason: "schema_behind", Message: "the database schema is behind this build: run the migrate command",
		Details: map[string]any{"missing": missing}}
}

// hostOutbox opens the connection of the forwarder (MFGRAPH_OPS_DSN, or
// MFGRAPH_DSN without it, always with the character set utf8mb4; see
// config.ForwarderDSN) and the sink of the host's outbox table. The
// connection is not tested here: /readyz reports whether the table can be
// reached, and the forwarder retries until it can.
func hostOutbox(cfg config.Config) (*sql.DB, *outbox.SQLSink, error) {
	dsn, err := cfg.ForwarderDSN()
	if err != nil {
		return nil, nil, err
	}
	conn, err := mysqldriver.NewConnector(dsn)
	if err != nil {
		return nil, nil, errors.New("the DSN of the event forwarder is not valid")
	}
	db := sql.OpenDB(conn)
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	sink, err := outbox.NewSQLSink(db, cfg.OutboxTable)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return db, sink, nil
}
