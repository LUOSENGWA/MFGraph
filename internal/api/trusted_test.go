package api_test

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/api"
	"github.com/LUOSENGWA/MFGraph/internal/ids"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

const (
	prefixT  = "X-Test-"
	tokenT   = "internal-token-0123456789abcdef"
	projectT = "01JD3X5Q8Z9W2V4T6R8P0N2P00"
)

// claimsT are valid identity headers of a module request.
func claimsT(kv ...string) map[string]string {
	h := map[string]string{prefixT + "Internal-Token": tokenT, prefixT + "Actor": "manager", prefixT + "Actor-Type": "human",
		prefixT + "Project": projectT}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "" {
			delete(h, kv[i])
		} else {
			h[kv[i]] = kv[i+1]
		}
	}
	return h
}

func trustedT(t *testing.T) *api.TrustedHeaders {
	t.Helper()
	a, err := api.NewTrustedHeaders(prefixT, prefixT+"Internal-Token", tokenT)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func requestT(h map[string]string) *http.Request {
	r := httptest.NewRequest("GET", "/x", nil)
	for k, v := range h {
		r.Header.Set(k, v)
	}
	return r
}

func TestTrustedHeaders(t *testing.T) {
	a := trustedT(t)
	worker := "worker-" + strings.Repeat("w", 253-len("worker-")) // 253 bytes, the longest canonical ref
	full := "@" + strings.Repeat("u", 255-len("@:example.com")) + ":example.com"
	p, err := a.Authenticate(requestT(claimsT(prefixT+"Actor", full, prefixT+"On-Behalf-Of", worker,
		prefixT+"On-Behalf-Of-Type", "worker", prefixT+"Team", "team-a", "Authorization", "Bearer whatever")))
	if err != nil {
		t.Fatal(err)
	}
	want := ports.Principal{Ref: full, ActorType: "human", ProjectScope: projectT, OnBehalfOfRef: worker, OnBehalfOfType: "worker",
		Team: "team-a"}
	if p != want {
		t.Fatalf("principal %+v\nwant %+v", p, want)
	}
	// On-Behalf-Of without a type; empty optional headers count as absent.
	p, err = a.Authenticate(requestT(claimsT(prefixT+"On-Behalf-Of", "alice")))
	if err != nil || p.OnBehalfOfRef != "alice" || p.OnBehalfOfType != "" {
		t.Fatalf("on behalf of without type: %+v %v", p, err)
	}
	r := requestT(claimsT())
	r.Header.Set(prefixT+"On-Behalf-Of", "")
	r.Header.Set(prefixT+"On-Behalf-Of-Type", "")
	if p, err := a.Authenticate(r); err != nil || p.OnBehalfOfRef != "" {
		t.Fatalf("empty optional headers: %+v %v", p, err)
	}

	bad := map[string]*http.Request{
		"no token":                requestT(claimsT(prefixT+"Internal-Token", "")),
		"wrong token":             requestT(claimsT(prefixT+"Internal-Token", tokenT+"x")),
		"token prefix":            requestT(claimsT(prefixT+"Internal-Token", tokenT[:10])),
		"bearer instead of token": requestT(claimsT(prefixT+"Internal-Token", "", "Authorization", "Bearer "+tokenT)),
		"no actor":                requestT(claimsT(prefixT+"Actor", "")),
		"actor not UTF-8":         requestT(claimsT(prefixT+"Actor", "man\xffager")),
		"actor of 256 bytes":      requestT(claimsT(prefixT+"Actor", strings.Repeat("a", 256))),
		"actor control character": requestT(claimsT(prefixT+"Actor", "a\x01b")),
		"no actor type":           requestT(claimsT(prefixT+"Actor-Type", "")),
		"actor type uppercase":    requestT(claimsT(prefixT+"Actor-Type", "Human")),
		"actor type of 17":        requestT(claimsT(prefixT+"Actor-Type", strings.Repeat("a", 17))),
		"no project":              requestT(claimsT(prefixT+"Project", "")),
		"project not a ULID":      requestT(claimsT(prefixT+"Project", "project-1")),
		"project lowercase":       requestT(claimsT(prefixT+"Project", strings.ToLower(projectT))),
		"on behalf of not UTF-8":  requestT(claimsT(prefixT+"On-Behalf-Of", "a\xff")),
		"on behalf of of 256":     requestT(claimsT(prefixT+"On-Behalf-Of", strings.Repeat("o", 256))),
		"on behalf of type bad":   requestT(claimsT(prefixT+"On-Behalf-Of", "alice", prefixT+"On-Behalf-Of-Type", "A B")),
		"type without on behalf":  requestT(claimsT(prefixT+"On-Behalf-Of-Type", "human")),
		"actor twice":             func() *http.Request { r := requestT(claimsT()); r.Header.Add(prefixT+"Actor", "other"); return r }(),
		"token twice": func() *http.Request {
			r := requestT(claimsT())
			r.Header.Add(prefixT+"Internal-Token", tokenT)
			return r
		}(),
		"project twice":           func() *http.Request { r := requestT(claimsT()); r.Header.Add(prefixT+"Project", projectT); return r }(),
		"standard prefix ignored": requestT(map[string]string{"X-Trusted-Internal-Token": tokenT, "X-Trusted-Actor": "a"}),
		"no headers":              requestT(nil),
		"on behalf of type twice": func() *http.Request {
			r := requestT(claimsT(prefixT+"On-Behalf-Of", "a", prefixT+"On-Behalf-Of-Type", "human"))
			r.Header.Add(prefixT+"On-Behalf-Of-Type", "human")
			return r
		}(),
		"actor type with a period": requestT(claimsT(prefixT+"Actor-Type", "a.b")),
	}
	for name, r := range bad {
		_, err := a.Authenticate(r)
		if !errors.Is(err, ports.ErrUnauthenticated) {
			t.Errorf("%s: err = %v", name, err)
			continue
		}
		if strings.Contains(err.Error(), tokenT) || strings.Contains(err.Error(), "manager") || strings.Contains(err.Error(), "aaaa") {
			t.Errorf("%s: the error repeats a value: %v", name, err)
		}
	}
	// The recorded headers: only valid external references.
	c := a.Claims(requestT(claimsT(prefixT+"Team", "t1", prefixT+"Token-Jti", "jti-1", prefixT+"Audience", "mfgraph",
		prefixT+"Issued-At", "2026-11-16T00:00:00Z")))
	if c != (api.RequestClaims{Audience: "mfgraph", Team: "t1", TokenJTI: "jti-1", IssuedAt: "2026-11-16T00:00:00Z"}) {
		t.Fatalf("claims: %+v", c)
	}
	if c := a.Claims(requestT(claimsT(prefixT+"Team", "a\x01", prefixT+"Token-Jti", strings.Repeat("j", 256)))); c.Team != "" || c.TokenJTI != "" {
		t.Fatalf("invalid recorded headers kept: %+v", c)
	}
	for _, args := range [][3]string{{"", "T", tokenT}, {prefixT, "", tokenT}, {prefixT, "T", ""}} {
		if _, err := api.NewTrustedHeaders(args[0], args[1], args[2]); err == nil {
			t.Errorf("NewTrustedHeaders%q accepted", args)
		}
	}
}

// TestTrustedHeadersOverHTTP: 401 bodies without a bearer challenge, the
// request ID of the host's proxy, and the access log of module mode.
func TestTrustedHeadersOverHTTP(t *testing.T) {
	logs := &strings.Builder{}
	h := api.NewHandler(api.Options{Authenticator: trustedT(t), IDGen: ids.NewGenerator(), TrustedHeaderPrefix: prefixT,
		APIPrefix: "/internal/v1/mfgraph", Logger: slog.New(slog.NewJSONHandler(logs, nil))})
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, b := do(t, srv, "GET", "/internal/v1/mfgraph/snapshots/"+ulid1, "", claimsT(prefixT+"Internal-Token", "wrong-token-value"))
	e := decodeEnvelope(t, resp, b)
	if resp.StatusCode != 401 || e.Code != api.CodeUnauthenticated || resp.Header.Get("WWW-Authenticate") != "" ||
		!strings.Contains(e.Message, "internal token") {
		t.Fatalf("wrong token: %d %s %q", resp.StatusCode, b, resp.Header.Get("WWW-Authenticate"))
	}
	resp, b = do(t, srv, "GET", "/internal/v1/mfgraph/snapshots/"+ulid1, "",
		claimsT(prefixT+"Actor-Type", "", prefixT+"Request-Id", "host-req-1", "X-Request-Id", "client-req-1"))
	e = decodeEnvelope(t, resp, b)
	if resp.StatusCode != 401 || !strings.Contains(e.Message, prefixT+"Actor-Type") || e.RequestID != "host-req-1" {
		t.Fatalf("missing actor type: %d %s", resp.StatusCode, b)
	}
	if !strings.Contains(logs.String(), "identity headers refused") || strings.Contains(logs.String(), tokenT) {
		t.Fatalf("log of a refused header:\n%s", logs)
	}
	// Authenticated (no service: 503), with the request and trace IDs of
	// the host's proxy; an invalid trusted request ID falls back to
	// X-Request-Id.
	logs.Reset()
	resp, b = do(t, srv, "GET", "/internal/v1/mfgraph/snapshots/"+ulid1, "",
		claimsT(prefixT+"Request-Id", "host-req-2", prefixT+"Trace-Id", "trace-abc", prefixT+"Team", "team-a",
			prefixT+"Token-Jti", "jti-9", prefixT+"On-Behalf-Of", "alice"))
	if resp.StatusCode != 503 || resp.Header.Get("X-Request-Id") != "host-req-2" {
		t.Fatalf("authenticated: %d %s %q", resp.StatusCode, b, resp.Header.Get("X-Request-Id"))
	}
	line := logs.String()
	for _, want := range []string{`"request_id":"host-req-2"`, `"trace_id":"trace-abc"`, `"actor_type":"human"`, `"project_id":"` + projectT + `"`,
		`"team":"team-a"`, `"token_jti":"jti-9"`, `"actor":"`, `"on_behalf_of":"`} {
		if !strings.Contains(line, want) {
			t.Errorf("access log lacks %s:\n%s", want, line)
		}
	}
	if strings.Contains(line, "manager") || strings.Contains(line, "alice") || strings.Contains(line, tokenT) {
		t.Errorf("the info log shows an actor reference or the token:\n%s", line)
	}
	resp, _ = do(t, srv, "GET", "/healthz", "", map[string]string{prefixT + "Request-Id": "bad id with spaces", "X-Request-Id": "client-3"})
	if resp.StatusCode != 200 || resp.Header.Get("X-Request-Id") != "client-3" {
		t.Fatalf("fallback request ID: %d %q", resp.StatusCode, resp.Header.Get("X-Request-Id"))
	}
	// Standalone handlers ignore the trusted request ID header.
	std := newServer(t)
	resp, _ = do(t, std, "GET", "/healthz", "", map[string]string{"X-Trusted-Request-Id": "host-req-4"})
	if resp.Header.Get("X-Request-Id") == "host-req-4" {
		t.Fatal("standalone mode used a trusted request ID")
	}
}
