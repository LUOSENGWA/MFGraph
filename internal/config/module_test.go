package config_test

import (
	"strings"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/config"
)

const (
	tenantM = "01JD3X5Q8Z9W2V4T6R8P0N2T00"
	dsnM    = "mfgraph:s3cret-pass@tcp(127.0.0.1:3306)/mfgraph"
	tokenM  = "module-internal-token-0123456789"
)

// moduleEnv is a complete module configuration with the host's names.
func moduleEnv() map[string]string {
	return map[string]string{
		config.EnvMode:                "module",
		config.EnvModuleID:            "mfgraph",
		config.EnvModuleSocket:        "/run/modules/mfgraph.sock",
		config.EnvModuleInternalToken: tokenM,
		config.EnvDSN:                 dsnM,
		config.EnvDataDir:             "/data/mfgraph",
		config.EnvTenantID:            tenantM,
	}
}

func with(m map[string]string, kv ...string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "" {
			delete(out, kv[i])
		} else {
			out[kv[i]] = kv[i+1]
		}
	}
	return out
}

func TestModuleDefaults(t *testing.T) {
	c, err := config.Load(env(moduleEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CheckServe(); err != nil {
		t.Fatal(err)
	}
	sock, ok := c.UnixSocket()
	if !c.Module() || !ok || sock != "/run/modules/mfgraph.sock" || c.APIPrefix != "/internal/v1/mfgraph" || c.KernelMigrations ||
		c.DBDSN != dsnM || c.OpsDSN != "" || c.StorageRoot != "/data/mfgraph" || c.TenantID != tenantM ||
		c.TrustedHeaderPrefix != "X-Trusted-" || c.InternalTokenHeader != "X-Trusted-Internal-Token" ||
		c.OutboxTable != "ops.module_outbox" || c.InternalToken != tokenM || len(c.Warnings()) != 0 {
		t.Fatalf("module defaults: %+v", c)
	}
	for _, v := range c.Summary() {
		if s, ok := v.(string); ok && (strings.Contains(s, tokenM) || strings.Contains(s, "s3cret")) {
			t.Fatalf("Summary leaks a secret: %v", c.Summary())
		}
	}
	// The host's settings.
	c, err = config.Load(env(with(moduleEnv(),
		config.EnvTrustedHeaderPrefix, "X-Host-", config.EnvOutboxTable, "ops_2.outbox_x", config.EnvOpsDSN, "ops:p@tcp(h:1)/ops",
		config.EnvRoutePrefix, "/internal/v1/files")))
	if err != nil {
		t.Fatal(err)
	}
	if c.TrustedHeaderPrefix != "X-Host-" || c.InternalTokenHeader != "X-Host-Internal-Token" || c.OutboxTable != "ops_2.outbox_x" ||
		c.OpsDSN != "ops:p@tcp(h:1)/ops" || c.APIPrefix != "/internal/v1/files" {
		t.Fatalf("module settings: %+v", c)
	}
	c, err = config.Load(env(with(moduleEnv(), config.EnvInternalTokenHeader, "X-Other-Token")))
	if err != nil || c.InternalTokenHeader != "X-Other-Token" {
		t.Fatalf("token header: %q %v", c.InternalTokenHeader, err)
	}
}

// TestModuleRefusals: settings that stop a module process from starting.
// Errors never contain the token or the DSN.
func TestModuleRefusals(t *testing.T) {
	load := map[string]map[string]string{
		"TCP address":                  with(moduleEnv(), config.EnvModuleSocket, "", config.EnvHTTPAddr, "127.0.0.1:8094"),
		"TCP address and a socket":     with(moduleEnv(), config.EnvHTTPAddr, "127.0.0.1:8094"),
		"kernel migrations":            with(moduleEnv(), config.EnvKernelMigrations, "true"),
		"wrong MODULE_ID":              with(moduleEnv(), config.EnvModuleID, "other"),
		"DSN aliases differ":           with(moduleEnv(), config.EnvDBDSN, "other:x@tcp(h:1)/x"),
		"storage aliases differ":       with(moduleEnv(), config.EnvStorageRoot, "/elsewhere"),
		"prefix aliases differ":        with(moduleEnv(), config.EnvRoutePrefix, "/a", config.EnvAPIPrefix, "/b"),
		"socket aliases differ":        with(moduleEnv(), config.EnvHTTPAddr, "unix:/other.sock"),
		"tenant not a ULID":            with(moduleEnv(), config.EnvTenantID, "tenant"),
		"bad mode":                     with(moduleEnv(), config.EnvMode, "embedded"),
		"bad outbox table":             with(moduleEnv(), config.EnvOutboxTable, "module_outbox"),
		"outbox table with backquote":  with(moduleEnv(), config.EnvOutboxTable, "ops.`x`"),
		"outbox table with three dots": with(moduleEnv(), config.EnvOutboxTable, "a.b.c"),
		"bad header prefix":            with(moduleEnv(), config.EnvTrustedHeaderPrefix, "X Atf-"),
		"bad token header":             with(moduleEnv(), config.EnvInternalTokenHeader, "X-Token:"),
		"token with white space":       with(moduleEnv(), config.EnvModuleInternalToken, " "+tokenM),
		"token with control character": with(moduleEnv(), config.EnvModuleInternalToken, tokenM+"\x01"),
	}
	for name, m := range load {
		_, err := config.Load(env(m))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), tokenM) || strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "x@tcp") {
			t.Errorf("%s: the error leaks a secret: %v", name, err)
		}
	}
	serve := map[string]map[string]string{
		"no socket":   with(moduleEnv(), config.EnvModuleSocket, ""),
		"no token":    with(moduleEnv(), config.EnvModuleInternalToken, ""),
		"no DSN":      with(moduleEnv(), config.EnvDSN, ""),
		"no data dir": with(moduleEnv(), config.EnvDataDir, ""),
		"no tenant":   with(moduleEnv(), config.EnvTenantID, ""),
		"zero tenant": with(moduleEnv(), config.EnvTenantID, "00000000000000000000000000"),
	}
	for name, m := range serve {
		c, err := config.Load(env(m))
		if err != nil {
			t.Errorf("%s: Load: %v", name, err)
			continue
		}
		if err := c.CheckServe(); err == nil {
			t.Errorf("%s: CheckServe accepted", name)
		} else if strings.Contains(err.Error(), tokenM) || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%s: the error leaks a secret: %v", name, err)
		}
	}
	// --socket fixes a missing socket.
	c, err := config.Load(env(with(moduleEnv(), config.EnvModuleSocket, "")))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetSocket("/run/x.sock"); err != nil || c.CheckServe() != nil || c.HTTPAddr != "unix:/run/x.sock" {
		t.Fatalf("--socket: %q %v %v", c.HTTPAddr, err, c.CheckServe())
	}
	if err := c.SetSocket(""); err == nil {
		t.Fatal("an empty --socket was accepted")
	}
}

// TestAliases: each name of a setting works alone, and both names with the
// same value are fine.
func TestAliases(t *testing.T) {
	for _, pair := range config.Aliases {
		for _, m := range []map[string]string{{pair[0]: "/x/y"}, {pair[1]: "/x/y"}, {pair[0]: "/x/y", pair[1]: "/x/y"}} {
			c, err := config.Load(env(m))
			if err != nil {
				t.Fatalf("%v: %v", m, err)
			}
			var got string
			switch pair[0] {
			case config.EnvDBDSN:
				got = c.DBDSN
			case config.EnvStorageRoot:
				got = c.StorageRoot
			case config.EnvAPIPrefix:
				got = c.APIPrefix
			}
			if got != "/x/y" {
				t.Errorf("%v: got %q", m, got)
			}
		}
	}
	for _, m := range []map[string]string{
		{config.EnvModuleSocket: "/s.sock"},
		{config.EnvModuleSocket: "/s.sock", config.EnvHTTPAddr: "unix:/s.sock"},
	} {
		c, err := config.Load(env(m))
		if sock, ok := c.UnixSocket(); err != nil || !ok || sock != "/s.sock" {
			t.Errorf("%v: %q %v", m, c.HTTPAddr, err)
		}
	}
}

// TestModuleWarnings: a short internal token and static tokens in module
// mode are accepted with a warning.
func TestModuleWarnings(t *testing.T) {
	c, err := config.Load(env(with(moduleEnv(), config.EnvModuleInternalToken, "dev-token",
		config.EnvAuthTokens, "token-aaaaaaaaaaaaaaaa=alice")))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CheckServe(); err != nil {
		t.Fatal(err)
	}
	w := strings.Join(c.Warnings(), "\n")
	if !strings.Contains(w, config.EnvModuleInternalToken) || !strings.Contains(w, config.EnvAuthTokens) || strings.Contains(w, "dev-token") {
		t.Fatalf("warnings: %q", w)
	}
	// Standalone mode ignores the module settings without warnings.
	c, err = config.Load(env(map[string]string{config.EnvModuleInternalToken: "dev-token"}))
	if err != nil || len(c.Warnings()) != 0 || c.Module() {
		t.Fatalf("standalone: %v %v", c.Warnings(), err)
	}
}

// TestLayered: the environment wins over the configuration file; an empty
// environment variable counts as unset.
func TestLayered(t *testing.T) {
	file := map[string]string{config.EnvMode: "module", config.EnvRoutePrefix: "/from/file", config.EnvLogLevel: "debug"}
	get := config.Layered(env(map[string]string{config.EnvRoutePrefix: "/from/env", config.EnvLogLevel: ""}), file)
	if get(config.EnvMode) != "module" || get(config.EnvRoutePrefix) != "/from/env" || get(config.EnvLogLevel) != "debug" ||
		get(config.EnvTenantID) != "" {
		t.Fatal("layering")
	}
	// Aliases conflict across the layers too.
	get = config.Layered(env(map[string]string{config.EnvAPIPrefix: "/from/env"}), file)
	if _, err := config.Load(get); err == nil {
		t.Fatal("conflicting aliases from the environment and the file were accepted")
	}
}

func TestParseFile(t *testing.T) {
	src := "\xef\xbb\xbf# module settings\r\n\r\n" +
		"MFGRAPH_MODE=module\n" +
		"  MFGRAPH_TRUSTED_HEADER_PREFIX = X-Host-  \n" +
		"\t# indented comment\n" +
		"MFGRAPH_ROUTE_PREFIX=/internal/v1/mfgraph\r\n" +
		"MFGRAPH_LOG_PSEUDONYM_KEY=abc#def=ghi\n" +
		"MFGRAPH_OPS_DSN=\n"
	m, err := config.ParseFile("module.env", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{config.EnvMode: "module", config.EnvTrustedHeaderPrefix: "X-Host-",
		config.EnvRoutePrefix: "/internal/v1/mfgraph", config.EnvLogPseudonymKey: "abc#def=ghi", config.EnvOpsDSN: ""}
	if len(m) != len(want) {
		t.Fatalf("parsed %v", m)
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %q, want %q", k, m[k], v)
		}
	}
	bad := map[string]string{
		"expansion":         "MFGRAPH_DSN=${DB_SECRET}\n",
		"expansion in key":  "MFGRAPH_${X}=1\n",
		"no equals sign":    "MFGRAPH_MODE\n",
		"unknown variable":  "MFGRAPH_LOG_LEVL=info\n",
		"export":            "export MFGRAPH_MODE=module\n",
		"duplicate":         "MFGRAPH_MODE=module\nMFGRAPH_MODE=module\n",
		"not a name":        "user:secretpw@tcp(h:1)/db?parseTime=true\n",
		"invalid UTF-8":     "MFGRAPH_MODE=mod\xffule\n",
		"lowercase unknown": "mfgraph_mode=module\n",
	}
	for name, src := range bad {
		_, err := config.ParseFile("module.env", []byte(src))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "SECRET") {
			t.Errorf("%s: the error repeats the value: %v", name, err)
		}
		if !strings.Contains(err.Error(), "module.env") {
			t.Errorf("%s: the error does not name the file: %v", name, err)
		}
	}
	// Every variable may appear in a file.
	var all strings.Builder
	for _, k := range config.Variables {
		all.WriteString(k + "=\n")
	}
	if m, err := config.ParseFile("all.env", []byte(all.String())); err != nil || len(m) != len(config.Variables) {
		t.Fatalf("all variables: %d %v", len(m), err)
	}
}
