package config

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/actorref"
)

// Environment variables.
const (
	EnvHTTPAddr         = "MFGRAPH_HTTP_ADDR"
	EnvDBDSN            = "MFGRAPH_DB_DSN"
	EnvMigrateOnStart   = "MFGRAPH_MIGRATE_ON_START"
	EnvKernelMigrations = "MFGRAPH_KERNEL_MIGRATIONS"
	EnvAuthTokens       = "MFGRAPH_AUTH_TOKENS"
	EnvLogLevel         = "MFGRAPH_LOG_LEVEL"
	EnvLogFormat        = "MFGRAPH_LOG_FORMAT"
	EnvShutdownTimeout  = "MFGRAPH_SHUTDOWN_TIMEOUT"
)

// MinTokenLength is the minimum length of a static bearer token.
const MinTokenLength = 16

// Config is the server configuration.
type Config struct {
	// HTTPAddr is the listen address (default 127.0.0.1:8080).
	HTTPAddr string
	// DBDSN is a go-sql-driver/mysql DSN, for example
	// "mfgraph:secret@tcp(127.0.0.1:3306)/mfgraph". Optional in this build:
	// without it the server starts, /readyz reports the database as
	// not_configured and business endpoints answer 501.
	DBDSN string
	// MigrateOnStart applies pending migrations before serving (default false).
	MigrateOnStart bool
	// KernelMigrations includes the kernel bootstrap tables (projects,
	// principals) when migrating. True for standalone deployments; set it to
	// false when a host platform owns those tables.
	KernelMigrations bool
	// AuthTokens maps static bearer tokens to actor references (any valid
	// actor reference: a name such as "alice", an ID such as
	// "@alice:example.com", or a principal's ULID). Format of
	// MFGRAPH_AUTH_TOKENS: "token=ACTOR_REF[,token=ACTOR_REF...]". Entries
	// are separated by ',', the first '=' of an entry separates the token
	// from the actor reference, and whitespace around an entry is ignored;
	// an actor reference given here therefore cannot contain ','.
	AuthTokens map[string]string
	// LogLevel and LogFormat ("json" or "text") configure log/slog.
	LogLevel  slog.Level
	LogFormat string
	// ShutdownTimeout bounds graceful shutdown (default 15s).
	ShutdownTimeout time.Duration
}

// Load reads the configuration from MFGRAPH_* variables through getenv
// (os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		HTTPAddr:         "127.0.0.1:8080",
		KernelMigrations: true,
		AuthTokens:       map[string]string{},
		LogLevel:         slog.LevelInfo,
		LogFormat:        "json",
		ShutdownTimeout:  15 * time.Second,
	}
	if v := getenv(EnvHTTPAddr); v != "" {
		c.HTTPAddr = v
	}
	c.DBDSN = getenv(EnvDBDSN)
	var err error
	if c.MigrateOnStart, err = parseBool(getenv, EnvMigrateOnStart, false); err != nil {
		return Config{}, err
	}
	if c.KernelMigrations, err = parseBool(getenv, EnvKernelMigrations, true); err != nil {
		return Config{}, err
	}
	if c.MigrateOnStart && c.DBDSN == "" {
		return Config{}, fmt.Errorf("config: %s requires %s", EnvMigrateOnStart, EnvDBDSN)
	}
	if c.AuthTokens, err = parseTokens(getenv(EnvAuthTokens)); err != nil {
		return Config{}, err
	}
	if v := getenv(EnvLogLevel); v != "" {
		if err := c.LogLevel.UnmarshalText([]byte(v)); err != nil {
			return Config{}, fmt.Errorf("config: %s: %v", EnvLogLevel, err)
		}
	}
	if v := getenv(EnvLogFormat); v != "" {
		if v != "json" && v != "text" {
			return Config{}, fmt.Errorf("config: %s must be json or text", EnvLogFormat)
		}
		c.LogFormat = v
	}
	if v := getenv(EnvShutdownTimeout); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("config: %s must be a positive duration such as 15s", EnvShutdownTimeout)
		}
		c.ShutdownTimeout = d
	}
	return c, nil
}

func parseBool(getenv func(string) string, name string, def bool) (bool, error) {
	v := getenv(name)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("config: %s must be true or false", name)
	}
	return b, nil
}

// parseTokens parses "token=ACTOR_REF,token=ACTOR_REF". Tokens are never
// echoed in errors.
func parseTokens(s string) (map[string]string, error) {
	out := map[string]string{}
	if strings.TrimSpace(s) == "" {
		return out, nil
	}
	for i, pair := range strings.Split(s, ",") {
		tok, ref, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok {
			return nil, fmt.Errorf("config: %s entry %d: want token=ACTOR_REF", EnvAuthTokens, i+1)
		}
		if len(tok) < MinTokenLength || strings.ContainsAny(tok, " \t\r\n") {
			return nil, fmt.Errorf("config: %s entry %d: token must have at least %d characters and no whitespace", EnvAuthTokens, i+1, MinTokenLength)
		}
		if err := actorref.Validate(ref); err != nil {
			return nil, fmt.Errorf("config: %s entry %d: %w", EnvAuthTokens, i+1, err)
		}
		if _, dup := out[tok]; dup {
			return nil, fmt.Errorf("config: %s entry %d: duplicate token", EnvAuthTokens, i+1)
		}
		out[tok] = ref
	}
	return out, nil
}

// Summary describes the configuration for logs without secrets.
func (c Config) Summary() []any {
	return []any{
		"http_addr", c.HTTPAddr,
		"db_configured", c.DBDSN != "",
		"migrate_on_start", c.MigrateOnStart,
		"kernel_migrations", c.KernelMigrations,
		"auth_tokens", len(c.AuthTokens),
		"log_level", c.LogLevel.String(),
	}
}
