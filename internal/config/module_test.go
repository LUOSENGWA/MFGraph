package config_test

import (
	"strings"
	"testing"
	"time"

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
		value := "/x/y"
		if pair[0] == config.EnvDBDSN {
			value = "u:p@tcp(h:1)/y"
		}
		for _, m := range []map[string]string{{pair[0]: value}, {pair[1]: value}, {pair[0]: value, pair[1]: value}} {
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
			if got != value {
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

// TestMalformedDSN: a DSN the driver cannot parse stops the start with a
// message that names the variable and repeats nothing of the DSN; the
// example of the review lacks the database name and has '/' in the
// password, which the driver's own message would quote in part.
func TestMalformedDSN(t *testing.T) {
	const bad = "zzops:ZzOps/Pw-77gg@tcp(127.0.0.1:33406)"
	for _, name := range []string{config.EnvDBDSN, config.EnvDSN, config.EnvOpsDSN} {
		for _, m := range []map[string]string{{name: bad}, with(moduleEnv(), config.EnvDSN, "", name, bad)} {
			if name == config.EnvOpsDSN && m[config.EnvMode] != "" {
				m[config.EnvDSN] = dsnM
			}
			_, err := config.Load(env(m))
			if err == nil {
				t.Errorf("%s=%s: accepted", name, bad)
				continue
			}
			msg := err.Error()
			if !strings.Contains(msg, name+" is not a valid DSN") {
				t.Errorf("%s: %v", name, err)
			}
			for _, part := range []string{"zzops", "ZzOps", "Pw-77gg", "33406"} {
				if strings.Contains(msg, part) {
					t.Errorf("%s: the error repeats %q: %v", name, part, err)
				}
			}
		}
	}
	for _, dsn := range []string{"u:p@tcp(h:1)/", "u:p/q@tcp(h:1)/db?x=1", "u@unix(/run/mysqld.sock)/db", "/db"} {
		if _, err := config.Load(env(map[string]string{config.EnvDBDSN: dsn, config.EnvOpsDSN: dsn})); err != nil {
			t.Errorf("%s: %v", dsn, err)
		}
	}
}

// TestForwarderDSN: the forwarder's connection gets charset=utf8mb4 unless
// the DSN sets it; any other character set stops the start of module mode
// (CheckServe) with a message that names the variable but not the DSN.
// Standalone mode has no forwarder and accepts any character set.
func TestForwarderDSN(t *testing.T) {
	for _, ok := range []string{"", "?charset=utf8mb4", "?charset=UTF8MB4", "?parseTime=true&collation=utf8mb4_bin",
		"?character_set_client=utf8mb4&collation_connection=%27utf8mb4_0900_ai_ci%27"} {
		c, err := config.Load(env(with(moduleEnv(), config.EnvOpsDSN, "ops:s3cret-pass@tcp(h:1)/ops"+ok)))
		if err != nil {
			t.Fatal(err)
		}
		mc, err := c.ForwarderDSN()
		if err != nil || c.CheckServe() != nil {
			t.Errorf("%q: %v %v", ok, err, c.CheckServe())
			continue
		}
		if f := mc.FormatDSN(); !strings.Contains(f, "charset=utf8mb4") || mc.User != "ops" || mc.DBName != "ops" {
			t.Errorf("%q: %s", ok, f)
		}
	}
	bad := map[string]string{
		"?charset=utf8":                       "character set utf8",
		"?charset=latin1":                     "character set latin1",
		"?charset=utf8,utf8mb4":               "list of character sets",
		"?charset=utf8mb4,utf8":               "list of character sets",
		"?parseTime=true&charset=utf8mb3":     "character set utf8mb3",
		"?collation=latin1_swedish_ci":        "collation latin1_swedish_ci",
		"?character_set_client=latin1":        "session variable character_set_client",
		"?collation_connection=utf8_bin":      "session variable collation_connection",
		"?charset=%27%3Bselect%20s3cret%27":   "(another value)",
		"?charset=utf8mb4&charset=latin1":     "character set latin1",
		"?character_set_results=%27latin1%27": "session variable character_set_results",
	}
	for q, says := range bad {
		for _, m := range []map[string]string{
			with(moduleEnv(), config.EnvOpsDSN, "ops:s3cret-pass@tcp(h:1)/ops"+q),
			with(moduleEnv(), config.EnvDSN, "", config.EnvDBDSN, dsnM+q), // no ops DSN: MFGRAPH_DB_DSN
		} {
			c, err := config.Load(env(m))
			if err != nil {
				t.Fatalf("%q: Load: %v", q, err)
			}
			err = c.CheckServe()
			name := config.EnvOpsDSN
			if m[config.EnvOpsDSN] == "" {
				name = config.EnvDBDSN
			}
			if err == nil || !strings.Contains(err.Error(), name+" ") || !strings.Contains(err.Error(), says) ||
				!strings.Contains(err.Error(), "charset=utf8mb4") {
				t.Errorf("%q (%s): %v", q, name, err)
				continue
			}
			if strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "@tcp") {
				t.Errorf("%q: the error repeats the DSN: %v", q, err)
			}
		}
	}
	// Standalone mode: no forwarder, any character set.
	c, err := config.Load(env(map[string]string{config.EnvDBDSN: dsnM + "?charset=utf8", config.EnvStorageRoot: "/data"}))
	if err != nil || c.CheckServe() != nil {
		t.Fatalf("standalone with charset=utf8: %v %v", err, c.CheckServe())
	}
}

// TestModuleShutdown: module mode refuses a shutdown timeout above 8 s (the
// host kills a module 8 s after SIGTERM) and keeps 1 s of it for the exit;
// standalone mode keeps its timeout as it is.
func TestModuleShutdown(t *testing.T) {
	for v, budget := range map[string]time.Duration{"": 7 * time.Second, "8s": 7 * time.Second, "4s": 3 * time.Second,
		"1s": 500 * time.Millisecond, "1500ms": 750 * time.Millisecond} {
		c, err := config.Load(env(with(moduleEnv(), config.EnvShutdownTimeout, v)))
		if err != nil || c.CheckServe() != nil || c.ShutdownBudget() != budget {
			t.Errorf("%q: budget %v, want %v; %v %v", v, c.ShutdownBudget(), budget, err, c.CheckServe())
		}
	}
	for _, v := range []string{"8001ms", "9s", "30s"} {
		c, err := config.Load(env(with(moduleEnv(), config.EnvShutdownTimeout, v)))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.CheckServe(); err == nil || !strings.Contains(err.Error(), config.EnvShutdownTimeout) ||
			!strings.Contains(err.Error(), "8s") {
			t.Errorf("%s: %v", v, err)
		}
	}
	c, err := config.Load(env(map[string]string{config.EnvShutdownTimeout: "30s"}))
	if err != nil || c.CheckServe() != nil || c.ShutdownBudget() != 30*time.Second {
		t.Fatalf("standalone 30s: %v %v %v", c.ShutdownBudget(), err, c.CheckServe())
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
