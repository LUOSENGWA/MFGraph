package config

import (
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/actorref"
	"github.com/LUOSENGWA/MFGraph/internal/ids"
)

// Environment variables. The MODULE_* variables and MFGRAPH_DSN,
// MFGRAPH_DATA_DIR and MFGRAPH_ROUTE_PREFIX are the names a host platform
// uses for a module process; MFGRAPH_DSN, MFGRAPH_DATA_DIR,
// MFGRAPH_ROUTE_PREFIX and MODULE_SOCKET are aliases of MFGRAPH_DB_DSN,
// MFGRAPH_STORAGE_ROOT, MFGRAPH_API_PREFIX and MFGRAPH_HTTP_ADDR=unix:<path>
// (see Aliases).
const (
	EnvMode                = "MFGRAPH_MODE"
	EnvHTTPAddr            = "MFGRAPH_HTTP_ADDR"
	EnvAPIPrefix           = "MFGRAPH_API_PREFIX"
	EnvRoutePrefix         = "MFGRAPH_ROUTE_PREFIX"
	EnvDBDSN               = "MFGRAPH_DB_DSN"
	EnvDSN                 = "MFGRAPH_DSN"
	EnvOpsDSN              = "MFGRAPH_OPS_DSN"
	EnvMigrateOnStart      = "MFGRAPH_MIGRATE_ON_START"
	EnvKernelMigrations    = "MFGRAPH_KERNEL_MIGRATIONS"
	EnvStorageRoot         = "MFGRAPH_STORAGE_ROOT"
	EnvDataDir             = "MFGRAPH_DATA_DIR"
	EnvTenantID            = "MFGRAPH_TENANT_ID"
	EnvMaxUploadBytes      = "MFGRAPH_MAX_UPLOAD_BYTES"
	EnvMaxJSONBodyBytes    = "MFGRAPH_MAX_JSON_BODY_BYTES"
	EnvMaxCommitChanges    = "MFGRAPH_MAX_COMMIT_CHANGES"
	EnvAuthTokens          = "MFGRAPH_AUTH_TOKENS"
	EnvLogLevel            = "MFGRAPH_LOG_LEVEL"
	EnvLogFormat           = "MFGRAPH_LOG_FORMAT"
	EnvLogPseudonymKey     = "MFGRAPH_LOG_PSEUDONYM_KEY"
	EnvShutdownTimeout     = "MFGRAPH_SHUTDOWN_TIMEOUT"
	EnvIdempotencyLease    = "MFGRAPH_IDEMPOTENCY_LEASE"
	EnvTrustedHeaderPrefix = "MFGRAPH_TRUSTED_HEADER_PREFIX"
	EnvInternalTokenHeader = "MFGRAPH_INTERNAL_TOKEN_HEADER"
	EnvOutboxTable         = "MFGRAPH_OUTBOX_TABLE"
	EnvModuleID            = "MODULE_ID"
	EnvModuleSocket        = "MODULE_SOCKET"
	EnvModuleInternalToken = "MODULE_INTERNAL_TOKEN"
)

// Variables lists every variable Load reads; a configuration file may set
// exactly these.
var Variables = []string{
	EnvMode, EnvHTTPAddr, EnvAPIPrefix, EnvRoutePrefix, EnvDBDSN, EnvDSN, EnvOpsDSN, EnvMigrateOnStart,
	EnvKernelMigrations, EnvStorageRoot, EnvDataDir, EnvTenantID, EnvMaxUploadBytes, EnvMaxJSONBodyBytes,
	EnvMaxCommitChanges, EnvAuthTokens, EnvLogLevel, EnvLogFormat, EnvLogPseudonymKey, EnvShutdownTimeout,
	EnvIdempotencyLease, EnvTrustedHeaderPrefix, EnvInternalTokenHeader, EnvOutboxTable, EnvModuleID, EnvModuleSocket,
	EnvModuleInternalToken,
}

// Aliases are pairs of names of one setting: the name MFGraph has always
// used and the name a host platform injects. Setting both to different
// values is an error. MODULE_SOCKET=<path> is a third alias, of
// MFGRAPH_HTTP_ADDR=unix:<path>.
var Aliases = [][2]string{
	{EnvDBDSN, EnvDSN},
	{EnvStorageRoot, EnvDataDir},
	{EnvAPIPrefix, EnvRoutePrefix},
}

// Modes (MFGRAPH_MODE).
const (
	// ModeStandalone: MFGraph is the whole service: it listens on TCP or a
	// Unix domain socket, authenticates static bearer tokens and keeps its
	// own projects (the kernel tables).
	ModeStandalone = "standalone"
	// ModeModule: MFGraph runs as a module process of a host platform: it
	// listens only on a Unix domain socket, trusts the identity headers the
	// host's proxy sets (authenticated by an internal token), checks only
	// that a resource belongs to the project of the request (the host owns
	// projects and their access control), and forwards its events to the
	// host's outbox table.
	ModeModule = "module"
)

// Defaults.
const (
	DefaultHTTPAddr        = "127.0.0.1:8094"
	DefaultAPIPrefix       = "/api/v1"
	DefaultModuleAPIPrefix = "/internal/v1/mfgraph"
	DefaultTenantID        = "00000000000000000000000000"
	DefaultMaxUpload       = 1 << 30
	// MaxUploadLimit is the largest value of MFGRAPH_MAX_UPLOAD_BYTES: 5 TiB,
	// the largest object of Amazon S3 and far below the 2^53-1 bytes a file
	// size of the content format can hold.
	MaxUploadLimit = 5 << 40
	// DefaultMaxJSONBody is the default of MFGRAPH_MAX_JSON_BODY_BYTES
	// (16 MiB); MinJSONBodyLimit and MaxJSONBodyLimit bound it (1 KiB to
	// 1 GiB).
	DefaultMaxJSONBody = 16 << 20
	MinJSONBodyLimit   = 1 << 10
	MaxJSONBodyLimit   = 1 << 30
	DefaultMaxChanges  = 10_000
	DefaultShutdown    = 8 * time.Second
	// DefaultIdempotencyLease is the default of MFGRAPH_IDEMPOTENCY_LEASE;
	// MinIdempotencyLease and MaxIdempotencyLease bound it (the upper bound
	// is the 24 hours an idempotency record is kept).
	DefaultIdempotencyLease = 60 * time.Second
	MinIdempotencyLease     = time.Second
	MaxIdempotencyLease     = 24 * time.Hour
	// ModuleID is the only value MODULE_ID may have.
	ModuleID = "mfgraph"
	// DefaultTrustedHeaderPrefix is the prefix of the identity headers in
	// module mode (MFGRAPH_TRUSTED_HEADER_PREFIX): a generic name; the host
	// platform configures its own.
	DefaultTrustedHeaderPrefix = "X-Trusted-"
	// DefaultOutboxTable is the host's outbox table in module mode
	// (MFGRAPH_OUTBOX_TABLE, schema.table).
	DefaultOutboxTable = "ops.module_outbox"
	// MinInternalTokenLength: a shorter MODULE_INTERNAL_TOKEN is accepted
	// with a warning.
	MinInternalTokenLength = 16
	unixPrefix             = "unix:"
	maxAPIPrefixBytes      = 128
	minPseudonymKeyBytes   = 16
	maxHeaderNameBytes     = 128
)

// MinTokenLength is the minimum length of a static bearer token.
const MinTokenLength = 16

var (
	headerNameRe  = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
	outboxTableRe = regexp.MustCompile(`^[A-Za-z0-9_]+\.[A-Za-z0-9_]+$`)
)

// Config is the server configuration.
type Config struct {
	// Mode is ModeStandalone (default) or ModeModule.
	Mode string
	// HTTPAddr is the listen address: host:port (default 127.0.0.1:8094 in
	// standalone mode), or "unix:<path>" for a Unix domain socket (see
	// UnixSocket); module mode accepts only a socket.
	HTTPAddr string
	// APIPrefix is the path prefix of the business endpoints (default
	// /api/v1, in module mode /internal/v1/mfgraph); /healthz and /readyz
	// are always at the root.
	APIPrefix string
	// DBDSN is a go-sql-driver/mysql DSN, for example
	// "mfgraph:secret@tcp(127.0.0.1:3306)/mfgraph". The service needs a
	// database: in standalone mode it starts without one, but /readyz and
	// every business endpoint answer 503 db_unavailable; module mode
	// requires it.
	DBDSN string
	// OpsDSN is the connection the event forwarder of module mode writes the
	// host's outbox table with (MFGRAPH_OPS_DSN); empty: DBDSN.
	OpsDSN string
	// StorageRoot is the root directory of the POSIX blob store; required
	// when DBDSN is set.
	StorageRoot string
	// TenantID is the tenant of every project and workspace (a ULID): in
	// standalone mode default 00000000000000000000000000; module mode
	// requires a fixed tenant ULID other than that one.
	TenantID string
	// MaxUploadBytes bounds one upload (default 1 GiB; 1 byte to 5 TiB).
	MaxUploadBytes int64
	// MaxJSONBodyBytes bounds the body of every request but an upload
	// (default 16 MiB; 1 KiB to 1 GiB). It also bounds the size of a
	// commit request.
	MaxJSONBodyBytes int64
	// MaxCommitChanges bounds the changes of one commit (default 10000).
	MaxCommitChanges int
	// MigrateOnStart applies pending migrations before serving (default false).
	MigrateOnStart bool
	// KernelMigrations includes the kernel bootstrap tables (projects,
	// principals) when migrating. True by default in standalone mode;
	// module mode never runs them (the host owns those tables) and refuses
	// MFGRAPH_KERNEL_MIGRATIONS=true.
	KernelMigrations bool
	// AuthTokens maps static bearer tokens to actor references (any valid
	// actor reference: a name such as "alice", an ID such as
	// "@alice:example.com", or a principal's ULID). Format of
	// MFGRAPH_AUTH_TOKENS: "token=ACTOR_REF[,token=ACTOR_REF...]". Entries
	// are separated by ',', the first '=' of an entry separates the token
	// from the actor reference, and whitespace around an entry is ignored;
	// an actor reference given here therefore cannot contain ','. Module
	// mode ignores them (with a warning).
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
	// IdempotencyLease is how long a request holds the in-progress record
	// of its Idempotency-Key; afterwards a request with the same key takes
	// it over (default 60s; 1s to 24h). Requests that run longer than the
	// lease can fail with conflict when the client retries meanwhile.
	IdempotencyLease time.Duration
	// ModuleID is MODULE_ID: empty or "mfgraph".
	ModuleID string
	// InternalToken is MODULE_INTERNAL_TOKEN, the secret the host's proxy
	// sends in InternalTokenHeader (module mode).
	InternalToken string
	// TrustedHeaderPrefix is the prefix of the identity headers of module
	// mode (default X-Trusted-): <prefix>Actor, <prefix>Actor-Type,
	// <prefix>Project, ...
	TrustedHeaderPrefix string
	// InternalTokenHeader is the header that carries InternalToken (default
	// <prefix>Internal-Token).
	InternalTokenHeader string
	// OutboxTable is the host's outbox table, schema.table (module mode;
	// default ops.module_outbox).
	OutboxTable string

	warnings []string
	// dbDSNName is the variable DBDSN came from (MFGRAPH_DB_DSN or its alias
	// MFGRAPH_DSN), for messages.
	dbDSNName string
}

// dbDSNVar names the variable of DBDSN in messages.
func (c Config) dbDSNVar() string {
	if c.dbDSNName != "" {
		return c.dbDSNName
	}
	return EnvDSN
}

// Module reports module mode.
func (c Config) Module() bool { return c.Mode == ModeModule }

// Warnings lists accepted settings that deserve a warning in the log, for
// example a short internal token. They never contain secrets.
func (c Config) Warnings() []string { return c.warnings }

// SetSocket makes the server listen on the Unix domain socket path: the
// --socket option, which wins over the environment and the configuration
// file.
func (c *Config) SetSocket(path string) error {
	if path == "" {
		return fmt.Errorf("config: --socket needs a path")
	}
	c.HTTPAddr = unixPrefix + path
	return nil
}

// CheckServe reports settings that the server needs in addition to what
// Load checks (the migrate command needs fewer). Standalone mode: with a
// database, a storage root. Module mode: a socket, the internal token, a
// database, a storage root, a tenant other than the all-zero default, and a
// forwarder connection with the character set utf8mb4 (ForwarderDSN).
func (c Config) CheckServe() error {
	if !c.Module() {
		if c.DBDSN != "" && c.StorageRoot == "" {
			return fmt.Errorf("config: %s is set, so %s is required", EnvDBDSN, EnvStorageRoot)
		}
		return nil
	}
	var missing []string
	if _, ok := c.UnixSocket(); !ok {
		missing = append(missing, "a Unix domain socket ("+EnvModuleSocket+" or --socket)")
	}
	if c.InternalToken == "" {
		missing = append(missing, EnvModuleInternalToken)
	}
	if c.DBDSN == "" {
		missing = append(missing, EnvDSN+" (or "+EnvDBDSN+")")
	}
	if c.StorageRoot == "" {
		missing = append(missing, EnvDataDir+" (or "+EnvStorageRoot+")")
	}
	if c.TenantID == "" {
		missing = append(missing, EnvTenantID)
	}
	if len(missing) > 0 {
		return fmt.Errorf("config: module mode requires %s", strings.Join(missing, ", "))
	}
	if c.TenantID == DefaultTenantID {
		return fmt.Errorf("config: module mode requires a fixed tenant ULID in %s, not the all-zero default", EnvTenantID)
	}
	if _, err := c.ForwarderDSN(); err != nil {
		return err
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

// Layered returns the lookup of the settings: a variable set (non-empty) in
// the environment wins over the configuration file. Command-line options
// win over both (SetSocket).
func Layered(getenv func(string) string, file map[string]string) func(string) string {
	return func(k string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return file[k]
	}
}

// alias returns the value of a setting that has two names; both set to
// different values is an error (the values are not repeated: they may be
// secrets).
func alias(getenv func(string) string, name, other string) (string, error) {
	a, b := getenv(name), getenv(other)
	if a != "" && b != "" && a != b {
		return "", fmt.Errorf("config: %s and %s name the same setting but have different values; set only one", name, other)
	}
	if a != "" {
		return a, nil
	}
	return b, nil
}

// Load reads the configuration through getenv: os.Getenv in production,
// layered over a configuration file (Layered).
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		Mode:                ModeStandalone,
		MaxUploadBytes:      DefaultMaxUpload,
		MaxJSONBodyBytes:    DefaultMaxJSONBody,
		MaxCommitChanges:    DefaultMaxChanges,
		AuthTokens:          map[string]string{},
		LogLevel:            slog.LevelInfo,
		LogFormat:           "json",
		ShutdownTimeout:     DefaultShutdown,
		IdempotencyLease:    DefaultIdempotencyLease,
		TrustedHeaderPrefix: DefaultTrustedHeaderPrefix,
		OutboxTable:         DefaultOutboxTable,
	}
	switch v := getenv(EnvMode); v {
	case "", ModeStandalone:
	case ModeModule:
		c.Mode = ModeModule
	default:
		return Config{}, fmt.Errorf("config: %s must be %s or %s", EnvMode, ModeStandalone, ModeModule)
	}
	module := c.Module()
	if c.ModuleID = getenv(EnvModuleID); c.ModuleID != "" && c.ModuleID != ModuleID {
		return Config{}, fmt.Errorf("config: %s must be %s when it is set", EnvModuleID, ModuleID)
	}

	// The listen address: MFGRAPH_HTTP_ADDR, or MODULE_SOCKET=<path> for
	// MFGRAPH_HTTP_ADDR=unix:<path>.
	c.HTTPAddr = getenv(EnvHTTPAddr)
	if sock := getenv(EnvModuleSocket); sock != "" {
		if c.HTTPAddr != "" && c.HTTPAddr != unixPrefix+sock {
			return Config{}, fmt.Errorf("config: %s and %s name the same setting (%s=<path> means %s=unix:<path>) "+
				"but have different values; set only one", EnvModuleSocket, EnvHTTPAddr, EnvModuleSocket, EnvHTTPAddr)
		}
		c.HTTPAddr = unixPrefix + sock
	}
	if p, ok := c.UnixSocket(); ok && p == "" {
		return Config{}, fmt.Errorf("config: %s: unix: needs a socket path", EnvHTTPAddr)
	}
	if _, ok := c.UnixSocket(); module && c.HTTPAddr != "" && !ok {
		return Config{}, fmt.Errorf("config: module mode listens only on a Unix domain socket: %s must not be a TCP address "+
			"(set %s or --socket)", EnvHTTPAddr, EnvModuleSocket)
	}
	if c.HTTPAddr == "" && !module {
		c.HTTPAddr = DefaultHTTPAddr
	}

	var err error
	if c.APIPrefix, err = alias(getenv, EnvAPIPrefix, EnvRoutePrefix); err != nil {
		return Config{}, err
	}
	if c.APIPrefix == "" {
		c.APIPrefix = DefaultAPIPrefix
		if module {
			c.APIPrefix = DefaultModuleAPIPrefix
		}
	} else if err := checkAPIPrefix(c.APIPrefix); err != nil {
		return Config{}, err
	}
	if c.DBDSN, err = alias(getenv, EnvDBDSN, EnvDSN); err != nil {
		return Config{}, err
	}
	c.dbDSNName = EnvDBDSN
	if getenv(EnvDBDSN) == "" {
		c.dbDSNName = EnvDSN
	}
	c.OpsDSN = getenv(EnvOpsDSN)
	// A DSN the driver cannot parse stops here, before its message (which
	// may quote a part of the password) could reach a log.
	for _, d := range [][2]string{{c.dbDSNName, c.DBDSN}, {EnvOpsDSN, c.OpsDSN}} {
		if d[1] != "" {
			if _, err := parseDSN(d[0], d[1]); err != nil {
				return Config{}, err
			}
		}
	}
	if c.StorageRoot, err = alias(getenv, EnvStorageRoot, EnvDataDir); err != nil {
		return Config{}, err
	}
	if v := getenv(EnvTenantID); v != "" {
		if !ids.Valid(v) {
			return Config{}, fmt.Errorf("config: %s must be a ULID (26 uppercase Crockford base32 characters)", EnvTenantID)
		}
		c.TenantID = v
	} else if !module {
		c.TenantID = DefaultTenantID
	}
	if c.MaxUploadBytes, err = parsePositive(getenv, EnvMaxUploadBytes, DefaultMaxUpload); err != nil || c.MaxUploadBytes > MaxUploadLimit {
		return Config{}, fmt.Errorf("config: %s must be an integer between 1 and %d (5 TiB)", EnvMaxUploadBytes, int64(MaxUploadLimit))
	}
	c.MaxJSONBodyBytes, err = parsePositive(getenv, EnvMaxJSONBodyBytes, DefaultMaxJSONBody)
	if err != nil || c.MaxJSONBodyBytes < MinJSONBodyLimit || c.MaxJSONBodyBytes > MaxJSONBodyLimit {
		return Config{}, fmt.Errorf("config: %s must be an integer between %d (1 KiB) and %d (1 GiB)", EnvMaxJSONBodyBytes,
			MinJSONBodyLimit, MaxJSONBodyLimit)
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
	if c.KernelMigrations, err = parseBool(getenv, EnvKernelMigrations, !module); err != nil {
		return Config{}, err
	}
	if module && c.KernelMigrations {
		return Config{}, fmt.Errorf("config: module mode never runs the kernel migrations (the host platform owns those "+
			"tables): %s must be false", EnvKernelMigrations)
	}
	if c.MigrateOnStart && c.DBDSN == "" {
		return Config{}, fmt.Errorf("config: %s requires %s", EnvMigrateOnStart, EnvDBDSN)
	}
	if c.AuthTokens, err = parseTokens(getenv(EnvAuthTokens)); err != nil {
		return Config{}, err
	}
	if module && len(c.AuthTokens) > 0 {
		c.warnings = append(c.warnings, EnvAuthTokens+" is ignored in module mode: the host platform authenticates callers")
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
	if v := getenv(EnvIdempotencyLease); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < MinIdempotencyLease || d > MaxIdempotencyLease {
			return Config{}, fmt.Errorf("config: %s must be a duration between 1s and 24h, such as 60s", EnvIdempotencyLease)
		}
		c.IdempotencyLease = d
	}
	if err := c.loadModule(getenv); err != nil {
		return Config{}, err
	}
	return c, nil
}

// loadModule reads the settings of module mode. They are checked in both
// modes, so that a typo is found before a deployment switches modes.
func (c *Config) loadModule(getenv func(string) string) error {
	c.InternalToken = getenv(EnvModuleInternalToken)
	if t := c.InternalToken; t != "" {
		if strings.TrimSpace(t) != t || strings.ContainsFunc(t, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return fmt.Errorf("config: %s must not start or end with white space or contain control characters", EnvModuleInternalToken)
		}
		if c.Module() && len(t) < MinInternalTokenLength {
			c.warnings = append(c.warnings, fmt.Sprintf("%s has fewer than %d characters; use a long random value",
				EnvModuleInternalToken, MinInternalTokenLength))
		}
	}
	if v := getenv(EnvTrustedHeaderPrefix); v != "" {
		if len(v) > maxHeaderNameBytes/2 || !headerNameRe.MatchString(v) {
			return fmt.Errorf("config: %s must consist of at most 64 characters A-Z a-z 0-9 -, for example X-Trusted-",
				EnvTrustedHeaderPrefix)
		}
		c.TrustedHeaderPrefix = v
	}
	c.InternalTokenHeader = c.TrustedHeaderPrefix + "Internal-Token"
	if v := getenv(EnvInternalTokenHeader); v != "" {
		if len(v) > maxHeaderNameBytes || !headerNameRe.MatchString(v) {
			return fmt.Errorf("config: %s must be a header name of at most 128 characters A-Z a-z 0-9 -", EnvInternalTokenHeader)
		}
		c.InternalTokenHeader = v
	}
	if v := getenv(EnvOutboxTable); v != "" {
		if !outboxTableRe.MatchString(v) || len(v) > 129 {
			return fmt.Errorf("config: %s must be schema.table, each part of A-Z a-z 0-9 _ (at most 64 characters), "+
				"for example ops.module_outbox", EnvOutboxTable)
		}
		c.OutboxTable = v
	}
	return nil
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
	s := []any{
		"mode", c.Mode,
		"http_addr", c.HTTPAddr,
		"api_prefix", c.APIPrefix,
		"db_configured", c.DBDSN != "",
		"migrate_on_start", c.MigrateOnStart,
		"kernel_migrations", c.KernelMigrations,
		"storage_root", c.StorageRoot,
		"tenant_id", c.TenantID,
		"max_upload_bytes", c.MaxUploadBytes,
		"max_json_body_bytes", c.MaxJSONBodyBytes,
		"max_commit_changes", c.MaxCommitChanges,
		"auth_tokens", len(c.AuthTokens),
		"log_level", c.LogLevel.String(),
		"log_pseudonym_key_configured", c.LogPseudonymKey != "",
		"shutdown_timeout", c.ShutdownTimeout.String(),
		"idempotency_lease", c.IdempotencyLease.String(),
	}
	if c.Module() {
		s = append(s,
			"module_id", c.ModuleID,
			"internal_token_configured", c.InternalToken != "",
			"trusted_header_prefix", c.TrustedHeaderPrefix,
			"internal_token_header", c.InternalTokenHeader,
			"ops_dsn_configured", c.OpsDSN != "",
			"outbox_table", c.OutboxTable,
		)
	}
	return s
}
