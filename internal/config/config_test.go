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
		config.EnvAuthTokens:       "token-aaaaaaaaaaaaaaaa=" + uidA + ", token-bbbbbbbbbbbbbbbb=" + uidA,
		config.EnvLogLevel:         "debug",
		config.EnvLogFormat:        "text",
		config.EnvShutdownTimeout:  "3s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":9090" || !c.MigrateOnStart || c.KernelMigrations || len(c.AuthTokens) != 2 ||
		c.AuthTokens["token-bbbbbbbbbbbbbbbb"] != uidA || c.LogLevel != slog.LevelDebug || c.LogFormat != "text" || c.ShutdownTimeout != 3*time.Second {
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
		"missing uid":        {config.EnvAuthTokens: "token-aaaaaaaaaaaaaaaa"},
		"bad uid":            {config.EnvAuthTokens: "token-aaaaaaaaaaaaaaaa=not-a-ulid"},
		"lowercase uid":      {config.EnvAuthTokens: "token-aaaaaaaaaaaaaaaa=" + strings.ToLower(uidA)},
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
