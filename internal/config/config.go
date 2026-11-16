package config

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/actorref"
	"github.com/LUOSENGWA/MFGraph/internal/ids"
)

// Environment variables.
const (
	EnvHTTPAddr          = "MFGRAPH_HTTP_ADDR"
	EnvAPIPrefix         = "MFGRAPH_API_PREFIX"
	EnvDBDSN             = "MFGRAPH_DB_DSN"
	EnvMigrateOnStart    = "MFGRAPH_MIGRATE_ON_START"
	EnvKernelMigrations  = "MFGRAPH_KERNEL_MIGRATIONS"
	EnvStorageRoot       = "MFGRAPH_STORAGE_ROOT"
	EnvTenantID          = "MFGRAPH_TENANT_ID"
	EnvMaxUploadBytes    = "MFGRAPH_MAX_UPLOAD_BYTES"
	EnvMaxCommitChanges  = "MFGRAPH_MAX_COMMIT_CHANGES"
	EnvAuthTokens        = "MFGRAPH_AUTH_TOKENS"
	EnvLogLevel          = "MFGRAPH_LOG_LEVEL"
	EnvLogFormat         = "MFGRAPH_LOG_FORMAT"
	EnvLogPseudonymKey   = "MFGRAPH_LOG_PSEUDONYM_KEY"
	EnvShutdownTimeout   = "MFGRAPH_SHUTDOWN_TIMEOUT"
	DefaultHTTPAddr      = "127.0.0.1:8094"
	DefaultAPIPrefix     = "/api/v1"
	DefaultTenantID      = "00000000000000000000000000"
	DefaultMaxUpload     = 1 << 30
	DefaultMaxChanges    = 10_000
	DefaultShutdown      = 8 * time.Second
	unixPrefix           = "unix:"
	maxAPIPrefixBytes    = 128
	minPseudonymKeyBytes = 16
)

// MinTokenLength is the minimum length of a static bearer token.
const MinTokenLength = 16

// Config is the server configuration.
type Config struct {
	// HTTPAddr is the listen address: host:port (default 127.0.0.1:8094),
	// or "unix:<path>" for a Unix domain socket (see UnixSocket).
	HTTPAddr string
	// APIPrefix is the path prefix of the business endpoints (default
	// /api/v1); /healthz and /readyz are always at the root. A host platform
	// that forwards a module path sets it to that path.
	APIPrefix string
	// DBDSN is a go-sql-driver/mysql DSN, for example
	// "mfgraph:secret@tcp(127.0.0.1:3306)/mfgraph". The service needs a
	// database: without one it starts, but /readyz and every business
	// endpoint answer 503 db_unavailable.
	DBDSN string
	// StorageRoot is the root directory of the POSIX blob store; required
	// when DBDSN is set.
	StorageRoot string
	// TenantID is the tenant of a standalone deployment (a ULID; default
	// 00000000000000000000000000).
	TenantID string
	// MaxUploadBytes bounds one upload (default 1 GiB).
	MaxUploadBytes int64
	// MaxCommitChanges bounds the changes of one commit (default 10000).
	MaxCommitChanges int
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
	// LogLevel and LogFormat ("json" or "text") configure log/slog. Logs at
	// info level and above identify a caller only by the pseudonym of its
	// actor reference ("actor", actorref.Pseudonymizer); raw actor
	// references appear only in debug-level records.
	LogLevel  slog.Level
	LogFormat string
	// LogPseudonymKey is the key of the log pseudonyms (HMAC-SHA256, at
	// least 16 bytes). Without it the server uses a random key per start,
	// so pseudonyms cannot be linked across restarts; changing the key has
	// the same effect.
	LogPseudonymKey string
	// ShutdownTimeout bounds graceful shutdown (default 8s, below the 10 s
	// after which a supervisor typically kills a process it sent SIGTERM).
	ShutdownTimeout time.Duration
}

// CheckServe reports settings that the server needs in addition to what
// Load checks: with a database, a storage root (migrate needs neither).
func (c Config) CheckServe() error {
	if c.DBDSN != "" && c.StorageRoot == "" {
		return fmt.Errorf("config: %s is set, so %s is required", EnvDBDSN, EnvStorageRoot)
	}
	return nil
}

// UnixSocket returns the socket path of a "unix:<path>" HTTPAddr.
func (c Config) UnixSocket() (string, bool) {
	if strings.HasPrefix(c.HTTPAddr, unixPrefix) {
		return strings.TrimPrefix(c.HTTPAddr, unixPrefix), true
	}
	return "", false
}

// Load reads the configuration from MFGRAPH_* variables through getenv
// (os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		HTTPAddr:         DefaultHTTPAddr,
		APIPrefix:        DefaultAPIPrefix,
		KernelMigrations: true,
		TenantID:         DefaultTenantID,
		MaxUploadBytes:   DefaultMaxUpload,
		MaxCommitChanges: DefaultMaxChanges,
		AuthTokens:       map[string]string{},
		LogLevel:         slog.LevelInfo,
		LogFormat:        "json",
		ShutdownTimeout:  DefaultShutdown,
	}
	if v := getenv(EnvHTTPAddr); v != "" {
		c.HTTPAddr = v
	}
	if p, ok := c.UnixSocket(); ok && p == "" {
		return Config{}, fmt.Errorf("config: %s: unix: needs a socket path", EnvHTTPAddr)
	}
	if v := getenv(EnvAPIPrefix); v != "" {
		if err := checkAPIPrefix(v); err != nil {
			return Config{}, err
		}
		c.APIPrefix = v
	}
	c.DBDSN = getenv(EnvDBDSN)
	c.StorageRoot = getenv(EnvStorageRoot)
	if v := getenv(EnvTenantID); v != "" {
		if !ids.Valid(v) {
			return Config{}, fmt.Errorf("config: %s must be a ULID (26 uppercase Crockford base32 characters)", EnvTenantID)
		}
		c.TenantID = v
	}
	var err error
	if c.MaxUploadBytes, err = parsePositive(getenv, EnvMaxUploadBytes, DefaultMaxUpload); err != nil {
		return Config{}, err
	}
	maxChanges, err := parsePositive(getenv, EnvMaxCommitChanges, DefaultMaxChanges)
	if err != nil || maxChanges > 1_000_000 {
		return Config{}, fmt.Errorf("config: %s must be an integer between 1 and 1000000", EnvMaxCommitChanges)
	}
	c.MaxCommitChanges = int(maxChanges)
	c.LogPseudonymKey = getenv(EnvLogPseudonymKey)
	if c.LogPseudonymKey != "" && len(c.LogPseudonymKey) < minPseudonymKeyBytes {
		return Config{}, fmt.Errorf("config: %s must have at least %d bytes", EnvLogPseudonymKey, minPseudonymKeyBytes)
	}
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
			return Config{}, fmt.Errorf("config: %s must be a positive duration such as 8s", EnvShutdownTimeout)
		}
		c.ShutdownTimeout = d
	}
	return c, nil
}

func parsePositive(getenv func(string) string, name string, def int64) (int64, error) {
	v := getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("config: %s must be a positive integer", name)
	}
	return n, nil
}

// checkAPIPrefix: "/" followed by '/'-separated segments of
// A-Z a-z 0-9 . _ ~ - (not "." or ".."), at most 128 bytes, no trailing
// '/', and not under /healthz or /readyz.
func checkAPIPrefix(p string) error {
	bad := func(why string) error {
		return fmt.Errorf("config: %s %q: %s (for example /api/v1)", EnvAPIPrefix, p, why)
	}
	if len(p) < 2 || p[0] != '/' || p[len(p)-1] == '/' || len(p) > maxAPIPrefixBytes {
		return bad("must start with '/', not end with '/' and have at most 128 bytes")
	}
	segs := strings.Split(p[1:], "/")
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return bad("empty, . or .. segment")
		}
		for i := 0; i < len(s); i++ {
			c := s[i]
			if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("._~-", c) >= 0) {
				return bad("segments may use only A-Z a-z 0-9 . _ ~ -")
			}
		}
	}
	if segs[0] == "healthz" || segs[0] == "readyz" {
		return bad("must not be below /healthz or /readyz")
	}
	return nil
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
		"api_prefix", c.APIPrefix,
		"db_configured", c.DBDSN != "",
		"migrate_on_start", c.MigrateOnStart,
		"kernel_migrations", c.KernelMigrations,
		"storage_root", c.StorageRoot,
		"tenant_id", c.TenantID,
		"max_upload_bytes", c.MaxUploadBytes,
		"max_commit_changes", c.MaxCommitChanges,
		"auth_tokens", len(c.AuthTokens),
		"log_level", c.LogLevel.String(),
		"log_pseudonym_key_configured", c.LogPseudonymKey != "",
		"shutdown_timeout", c.ShutdownTimeout.String(),
	}
}
