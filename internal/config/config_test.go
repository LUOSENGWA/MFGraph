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
	if c.HTTPAddr != "127.0.0.1:8080" || c.DBDSN != "" || c.MigrateOnStart || !c.KernelMigrations ||
		len(c.AuthTokens) != 0 || c.LogLevel != slog.LevelInfo || c.LogFormat != "json" || c.ShutdownTimeout != 15*time.Second {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestAllVariables(t *testing.T) {
	c, err := config.Load(env(map[string]string{
		config.EnvHTTPAddr:         ":9090",
		config.EnvDBDSN:            "u:p@tcp(127.0.0.1:3306)/mfgraph",
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
	if c.HTTPAddr != ":9090" || !c.MigrateOnStart || c.KernelMigrations || len(c.AuthTokens) != 3 ||
		c.AuthTokens["token-bbbbbbbbbbbbbbbb"] != uidA || c.AuthTokens["token-cccccccccccccccc"] != "alice" ||
		c.LogLevel != slog.LevelDebug || c.LogFormat != "text" || c.ShutdownTimeout != 3*time.Second {
		t.Fatalf("parsed: %+v", c)
	}
	for _, v := range c.Summary() {
		if s, ok := v.(string); ok && strings.Contains(s, "token-") {
			t.Fatal("Summary leaks a token")
		}
	}
}

func TestInvalid(t *testing.T) {
	cases := map[string]map[string]string{
		"bad bool":           {config.EnvMigrateOnStart: "yes please"},
		"migrate without db": {config.EnvMigrateOnStart: "true"},
		"short token":        {config.EnvAuthTokens: "short=" + uidA},
		"missing actor":      {config.EnvAuthTokens: "token-aaaaaaaaaaaaaaaa"},
		"empty actor":        {config.EnvAuthTokens: "token-aaaaaaaaaaaaaaaa="},
		"actor too long":     {config.EnvAuthTokens: "token-aaaaaaaaaaaaaaaa=" + strings.Repeat("x", 256)},
		"control character":  {config.EnvAuthTokens: "token-aaaaaaaaaaaaaaaa=a\x01b"},
		"invalid utf-8":      {config.EnvAuthTokens: "token-aaaaaaaaaaaaaaaa=a\xffb"},
		"duplicate token":    {config.EnvAuthTokens: "token-aaaaaaaaaaaaaaaa=" + uidA + ",token-aaaaaaaaaaaaaaaa=" + uidA},
		"bad level":          {config.EnvLogLevel: "loud"},
		"bad format":         {config.EnvLogFormat: "xml"},
		"bad timeout":        {config.EnvShutdownTimeout: "-1s"},
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
