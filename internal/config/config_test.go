package config_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/config"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

const uidA = "01JD3X5Q8Z9W2V4T6R8P0N2M4K"

func TestDefaults(t *testing.T) {
	c, err := config.Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != "127.0.0.1:8094" || c.APIPrefix != "/api/v1" || c.DBDSN != "" || c.MigrateOnStart || !c.KernelMigrations ||
		c.StorageRoot != "" || c.TenantID != "00000000000000000000000000" || c.MaxUploadBytes != 1<<30 || c.MaxCommitChanges != 10000 ||
		len(c.AuthTokens) != 0 || c.LogLevel != slog.LevelInfo || c.LogFormat != "json" || c.LogPseudonymKey != "" ||
		c.ShutdownTimeout != 8*time.Second {
		t.Fatalf("defaults: %+v", c)
	}
	if _, ok := c.UnixSocket(); ok {
		t.Fatal("the default address is not a socket")
	}
}

func TestAllVariables(t *testing.T) {
	c, err := config.Load(env(map[string]string{
		config.EnvHTTPAddr:         "unix:/run/mfgraph/api.sock",
		config.EnvAPIPrefix:        "/internal/v1/files",
		config.EnvDBDSN:            "u:p@tcp(127.0.0.1:3306)/mfgraph",
		config.EnvStorageRoot:      "/var/lib/mfgraph",
		config.EnvTenantID:         uidA,
		config.EnvMaxUploadBytes:   "1048576",
		config.EnvMaxCommitChanges: "50",
		config.EnvLogPseudonymKey:  "0123456789abcdef-secret",
		config.EnvMigrateOnStart:   "true",
		config.EnvKernelMigrations: "false",
		config.EnvAuthTokens:       "token-aaaaaaaaaaaaaaaa=" + uidA + ", token-bbbbbbbbbbbbbbbb=" + uidA + ",token-cccccccccccccccc=alice",
		config.EnvLogLevel:         "debug",
		config.EnvLogFormat:        "text",
		config.EnvShutdownTimeout:  "3s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if sock, ok := c.UnixSocket(); !ok || sock != "/run/mfgraph/api.sock" || c.APIPrefix != "/internal/v1/files" ||
		c.StorageRoot != "/var/lib/mfgraph" || c.TenantID != uidA || c.MaxUploadBytes != 1<<20 || c.MaxCommitChanges != 50 {
		t.Fatalf("parsed: %+v", c)
	}
	if !c.MigrateOnStart || c.KernelMigrations || len(c.AuthTokens) != 3 ||
		c.AuthTokens["token-bbbbbbbbbbbbbbbb"] != uidA || c.AuthTokens["token-cccccccccccccccc"] != "alice" ||
		c.LogLevel != slog.LevelDebug || c.LogFormat != "text" || c.ShutdownTimeout != 3*time.Second {
		t.Fatalf("parsed: %+v", c)
	}
	for _, v := range c.Summary() {
		if s, ok := v.(string); ok && (strings.Contains(s, "token-") || strings.Contains(s, "secret") || strings.Contains(s, "u:p@")) {
			t.Fatal("Summary leaks a secret")
		}
	}
}

func TestInvalid(t *testing.T) {
	cases := map[string]map[string]string{
		"bad bool":                {config.EnvMigrateOnStart: "yes please"},
		"migrate without db":      {config.EnvMigrateOnStart: "true"},
		"short token":             {config.EnvAuthTokens: "short=" + uidA},
		"missing actor":           {config.EnvAuthTokens: "token-aaaaaaaaaaaaaaaa"},
		"empty actor":             {config.EnvAuthTokens: "token-aaaaaaaaaaaaaaaa="},
		"actor too long":          {config.EnvAuthTokens: "token-aaaaaaaaaaaaaaaa=" + strings.Repeat("x", 256)},
		"control character":       {config.EnvAuthTokens: "token-aaaaaaaaaaaaaaaa=a\x01b"},
		"invalid utf-8":           {config.EnvAuthTokens: "token-aaaaaaaaaaaaaaaa=a\xffb"},
		"duplicate token":         {config.EnvAuthTokens: "token-aaaaaaaaaaaaaaaa=" + uidA + ",token-aaaaaaaaaaaaaaaa=" + uidA},
		"bad level":               {config.EnvLogLevel: "loud"},
		"bad format":              {config.EnvLogFormat: "xml"},
		"bad timeout":             {config.EnvShutdownTimeout: "-1s"},
		"empty socket path":       {config.EnvHTTPAddr: "unix:"},
		"prefix without /":        {config.EnvAPIPrefix: "api/v1"},
		"prefix with /":           {config.EnvAPIPrefix: "/api/v1/"},
		"prefix //":               {config.EnvAPIPrefix: "/api//v1"},
		"prefix ..":               {config.EnvAPIPrefix: "/api/../v1"},
		"prefix space":            {config.EnvAPIPrefix: "/api v1"},
		"prefix /":                {config.EnvAPIPrefix: "/"},
		"prefix healthz":          {config.EnvAPIPrefix: "/healthz/v1"},
		"prefix too long":         {config.EnvAPIPrefix: "/" + strings.Repeat("a", 128)},
		"tenant not a ULID":       {config.EnvTenantID: "tenant-1"},
		"tenant lowercase":        {config.EnvTenantID: strings.ToLower(uidA)},
		"upload limit 0":          {config.EnvMaxUploadBytes: "0"},
		"upload limit int64":      {config.EnvMaxUploadBytes: "9223372036854775807"},
		"upload limit over 5 TiB": {config.EnvMaxUploadBytes: "5497558138881"},
		"upload limit text":       {config.EnvMaxUploadBytes: "1GiB"},
		"changes 0":               {config.EnvMaxCommitChanges: "0"},
		"changes huge":            {config.EnvMaxCommitChanges: "2000000"},
		"short pseudonym key":     {config.EnvLogPseudonymKey: "short"},
	}
	for name, m := range cases {
		_, err := config.Load(env(m))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), "token-aaaaaaaaaaaaaaaa") {
			t.Errorf("%s: error message leaks the token: %v", name, err)
		}
	}
}

// TestActorRefTokens: a static token maps to any valid actor reference, not
// only to a ULID.
func TestActorRefTokens(t *testing.T) {
	worker := "worker-" + strings.Repeat("a", 243) + "-01"                        // 253 characters
	id255 := "@" + strings.Repeat("u", 255-len("@:example.com")) + ":example.com" // 255 bytes
	for _, ref := range []string{"manager", "alice", worker, "@alice:example.com", "@a=b:example.com", id255, uidA,
		strings.ToLower(uidA), "not-a-ulid"} {
		c, err := config.Load(env(map[string]string{config.EnvAuthTokens: "token-aaaaaaaaaaaaaaaa=" + ref}))
		if err != nil {
			t.Errorf("actor %q: %v", ref, err)
			continue
		}
		if got := c.AuthTokens["token-aaaaaaaaaaaaaaaa"]; got != ref {
			t.Errorf("actor %q parsed as %q", ref, got)
		}
	}
}

func TestCheckServe(t *testing.T) {
	c, err := config.Load(env(map[string]string{config.EnvDBDSN: "u:p@tcp(127.0.0.1:3306)/x"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CheckServe(); err == nil || !strings.Contains(err.Error(), config.EnvStorageRoot) {
		t.Fatalf("a database without a storage root: %v", err)
	}
	c.StorageRoot = "/data"
	if err := c.CheckServe(); err != nil {
		t.Fatal(err)
	}
	if err := (config.Config{}).CheckServe(); err != nil {
		t.Fatal(err)
	}
}

func TestUploadLimitRange(t *testing.T) {
	for v, want := range map[string]int64{"1": 1, "5497558138880": 5 << 40, "1073741824": 1 << 30} {
		c, err := config.Load(env(map[string]string{config.EnvMaxUploadBytes: v}))
		if err != nil || c.MaxUploadBytes != want {
			t.Errorf("%s: %d %v", v, c.MaxUploadBytes, err)
		}
	}
}
