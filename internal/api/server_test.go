package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/actorref"
	"github.com/LUOSENGWA/MFGraph/internal/api"
	"github.com/LUOSENGWA/MFGraph/internal/ids"
)

const (
	token = "test-token-aaaaaaaaaaaaaaaa"
	uidA  = "01JD3X5Q8Z9W2V4T6R8P0N2M4K"
	ulid1 = "01JD3X5Q8Z9W2V4T6R8P0N2M4A"
	hashA = "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
)

func newServer(t *testing.T, checks ...api.ReadinessCheck) *httptest.Server {
	t.Helper()
	authn, err := api.NewStaticTokens(map[string]string{token: uidA})
	if err != nil {
		t.Fatal(err)
	}
	h := api.NewHandler(api.Options{
		Authenticator: authn,
		IDGen:         ids.NewGenerator(),
		Readiness:     checks,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:       "test-build",
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

type envelope struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id"`
	Details   map[string]any `json:"details"`
}

func do(t *testing.T, srv *httptest.Server, method, path, body string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func decodeEnvelope(t *testing.T, resp *http.Response, b []byte) envelope {
	t.Helper()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content type %q, body %s", ct, b)
	}
	var e envelope
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields() // the envelope is flat: no "error" wrapper
	if err := dec.Decode(&e); err != nil {
		t.Fatalf("decode envelope %s: %v", b, err)
	}
	if e.Code == "" || e.Message == "" {
		t.Fatalf("envelope without code/message: %s", b)
	}
	if e.RequestID == "" || e.RequestID != resp.Header.Get("X-Request-Id") {
		t.Fatalf("request_id %q does not match header %q", e.RequestID, resp.Header.Get("X-Request-Id"))
	}
	return e
}

var auth = map[string]string{"Authorization": "Bearer " + token}

func TestHealthz(t *testing.T) {
	srv := newServer(t)
	resp, b := do(t, srv, "GET", "/healthz", "", nil)
	if resp.StatusCode != 200 || strings.TrimSpace(string(b)) != `{"status":"ok"}` {
		t.Fatalf("healthz: %d %s", resp.StatusCode, b)
	}
	if !ids.Valid(resp.Header.Get("X-Request-Id")) {
		t.Fatalf("generated request id %q is not a ULID", resp.Header.Get("X-Request-Id"))
	}
}

func TestReadyz(t *testing.T) {
	srv := newServer(t, api.ReadinessCheck{Name: "storage", Code: api.CodeStorageUnavailable})
	resp, b := do(t, srv, "GET", "/readyz", "", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"storage":"not_configured"`) {
		t.Fatalf("readyz: %d %s", resp.StatusCode, b)
	}
	// The database is required: not configured is not ready.
	srv = newServer(t, api.ReadinessCheck{Name: "db", Code: api.CodeDBUnavailable, Required: true})
	resp, b = do(t, srv, "GET", "/readyz", "", nil)
	if e := decodeEnvelope(t, resp, b); resp.StatusCode != 503 || e.Code != api.CodeDBUnavailable || e.Details["reason"] != "not_configured" {
		t.Fatalf("readyz without a database: %d %s", resp.StatusCode, b)
	}
	srv = newServer(t, api.ReadinessCheck{Name: "db", Code: api.CodeDBUnavailable, Check: func(context.Context) error { return errors.New("down") }})
	resp, b = do(t, srv, "GET", "/readyz", "", nil)
	if resp.StatusCode != 503 || decodeEnvelope(t, resp, b).Code != api.CodeDBUnavailable {
		t.Fatalf("readyz down: %d %s", resp.StatusCode, b)
	}
	srv = newServer(t, api.ReadinessCheck{Name: "db", Code: api.CodeDBUnavailable, Check: func(context.Context) error { return nil }})
	if resp, b = do(t, srv, "GET", "/readyz", "", nil); resp.StatusCode != 200 || !strings.Contains(string(b), `"db":"ok"`) ||
		!strings.Contains(string(b), `"unicode_version":"15.0.0"`) || !strings.Contains(string(b), `"version":"test-build"`) {
		t.Fatalf("readyz ok: %d %s", resp.StatusCode, b)
	}
}

func TestRequestIDPropagation(t *testing.T) {
	srv := newServer(t)
	resp, _ := do(t, srv, "GET", "/healthz", "", map[string]string{"X-Request-Id": "client-req-42"})
	if got := resp.Header.Get("X-Request-Id"); got != "client-req-42" {
		t.Fatalf("valid request id not reused: %q", got)
	}
	for _, bad := range []string{"has space", strings.Repeat("x", 129), "tab\there"} {
		resp, _ := do(t, srv, "GET", "/healthz", "", map[string]string{"X-Request-Id": bad})
		if got := resp.Header.Get("X-Request-Id"); !ids.Valid(got) {
			t.Fatalf("invalid request id %q not replaced: %q", bad, got)
		}
	}
}

func TestAuthentication(t *testing.T) {
	srv := newServer(t)
	for name, hdr := range map[string]map[string]string{
		"missing":   nil,
		"wrong":     {"Authorization": "Bearer nope-nope-nope-nope"},
		"basic":     {"Authorization": "Basic " + token},
		"no-token":  {"Authorization": "Bearer "},
		"two-parts": {"Authorization": "Bearer " + token + " extra"},
	} {
		resp, b := do(t, srv, "GET", "/api/v1/workspaces/"+ulid1, "", hdr)
		if resp.StatusCode != 401 || decodeEnvelope(t, resp, b).Code != api.CodeUnauthenticated {
			t.Errorf("%s: %d %s", name, resp.StatusCode, b)
		}
		if !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Bearer") {
			t.Errorf("%s: missing WWW-Authenticate", name)
		}
	}
	// Scheme is case-insensitive (the server has no database: 503).
	resp, _ := do(t, srv, "GET", "/api/v1/workspaces/"+ulid1, "", map[string]string{"Authorization": "bearer " + token})
	if resp.StatusCode != 503 {
		t.Fatalf("lowercase scheme: %d", resp.StatusCode)
	}
}

// TestEveryOperationAnswers503WithoutDatabase sends a valid request to every
// operation of the contract (except health) to a server without a database
// and expects 503 db_unavailable: the service needs a database.
func TestEveryOperationAnswers503WithoutDatabase(t *testing.T) {
	srv := newServer(t)
	commit := `{"branch_id":"` + ulid1 + `","expected_generation":1,"message":"m","changes":[{"path":"a.txt","hash":"` + hashA + `"}]}`
	cases := []struct{ op, method, path, body, ctype string }{
		{"createProject", "POST", "/api/v1/projects", `{"name":"demo"}`, "application/json"},
		{"getProject", "GET", "/api/v1/projects/" + ulid1, "", ""},
		{"createWorkspace", "POST", "/api/v1/workspaces", `{"project_id":"` + ulid1 + `"}`, "application/json"},
		{"getWorkspace", "GET", "/api/v1/workspaces/" + ulid1, "", ""},
		{"listBranches", "GET", "/api/v1/workspaces/" + ulid1 + "/branches?limit=10", "", ""},
		{"putObject", "PUT", "/api/v1/workspaces/" + ulid1 + "/objects", "hello\n", "application/octet-stream"},
		{"uploadObject", "PUT", "/api/v1/objects/" + ulid1, "hello\n", "application/octet-stream"},
		{"createCommit", "POST", "/api/v1/workspaces/" + ulid1 + "/commits", commit, "application/json"},
		{"getTimeline", "GET", "/api/v1/workspaces/" + ulid1 + "/timeline?branch_id=" + ulid1, "", ""},
		{"getSnapshot", "GET", "/api/v1/snapshots/" + ulid1, "", ""},
		{"listSnapshotTree", "GET", "/api/v1/snapshots/" + ulid1 + "/tree?path=docs", "", ""},
		{"getSnapshotFile", "GET", "/api/v1/snapshots/" + ulid1 + "/file?path=a.txt", "", ""},
		{"getSnapshotDiff", "GET", "/api/v1/snapshots/" + ulid1 + "/diff?base=" + ulid1, "", ""},
		{"restoreSnapshot", "POST", "/api/v1/snapshots/" + ulid1 + "/restore", `{"branch_id":"` + ulid1 + `","expected_generation":2}`, "application/json"},
	}
	if len(cases) != len(wantOperations)-2 {
		t.Fatalf("test covers %d operations, contract has %d", len(cases), len(wantOperations)-2)
	}
	for _, c := range cases {
		hdr := map[string]string{"Authorization": "Bearer " + token, "Idempotency-Key": "k-1"}
		if c.ctype != "" {
			hdr["Content-Type"] = c.ctype
		}
		resp, b := do(t, srv, c.method, c.path, c.body, hdr)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s: status %d, body %s", c.op, resp.StatusCode, b)
			continue
		}
		e := decodeEnvelope(t, resp, b)
		if e.Code != api.CodeDBUnavailable || e.Details["reason"] != "not_configured" {
			t.Errorf("%s: envelope %+v", c.op, e)
		}
	}
}

func TestInvalidRequestsUseTheEnvelope(t *testing.T) {
	srv := newServer(t)
	hdr := map[string]string{"Authorization": "Bearer " + token, "Content-Type": "application/json"}
	resp, b := do(t, srv, "POST", "/api/v1/projects", `{"name":`, hdr)
	if resp.StatusCode != 400 || decodeEnvelope(t, resp, b).Code != api.CodeInvalidRequest {
		t.Fatalf("bad JSON: %d %s", resp.StatusCode, b)
	}
	resp, b = do(t, srv, "GET", "/api/v1/workspaces/"+ulid1+"/branches?limit=abc", "", auth)
	if resp.StatusCode != 400 || decodeEnvelope(t, resp, b).Code != api.CodeInvalidRequest {
		t.Fatalf("bad limit: %d %s", resp.StatusCode, b)
	}
	resp, b = do(t, srv, "GET", "/api/v1/snapshots/"+ulid1+"/file", "", auth)
	if resp.StatusCode != 400 || decodeEnvelope(t, resp, b).Code != api.CodeInvalidRequest {
		t.Fatalf("missing required path query: %d %s", resp.StatusCode, b)
	}
	resp, b = do(t, srv, "GET", "/api/v1/nope", "", auth)
	if resp.StatusCode != 404 || decodeEnvelope(t, resp, b).Code != api.CodeNotFound {
		t.Fatalf("unknown route: %d %s", resp.StatusCode, b)
	}
	resp, b = do(t, srv, "DELETE", "/api/v1/workspaces/"+ulid1, "", auth)
	if resp.StatusCode != 404 || decodeEnvelope(t, resp, b).Code != api.CodeNotFound {
		t.Fatalf("unsupported method: %d %s", resp.StatusCode, b)
	}
}

// TestAccessLogHidesActorReferences: info-level records carry the caller only
// as "actor", the pseudonym of its actor reference; the raw reference (and
// the principal UID, which equals a ULID reference) appears only in
// debug-level records.
func TestAccessLogHidesActorReferences(t *testing.T) {
	const tokB = "test-token-bbbbbbbbbbbbbbbb"
	refs := map[string]string{token: "alice.private@example.com", tokB: uidA} // a plain name and a ULID
	authn, err := api.NewStaticTokens(refs)
	if err != nil {
		t.Fatal(err)
	}
	pseudonymizer, err := actorref.NewPseudonymizer([]byte("test-pseudonym-key-0123"))
	if err != nil {
		t.Fatal(err)
	}
	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
		var buf bytes.Buffer
		h := api.NewHandler(api.Options{
			Authenticator: authn,
			IDGen:         ids.NewGenerator(),
			Logger:        slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: level})),
			Pseudonym:     pseudonymizer.Pseudonym,
		})
		srv := httptest.NewServer(h)
		for tok := range refs {
			do(t, srv, "GET", "/api/v1/projects/"+ulid1, "", map[string]string{"Authorization": "Bearer " + tok})
		}
		srv.Close()
		seen := map[string]bool{}
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("log line %q: %v", line, err)
			}
			for _, ref := range refs {
				if strings.Contains(line, ref) && rec["level"] != "DEBUG" {
					t.Errorf("level %v: a %v record contains the raw actor reference %q: %s", level, rec["level"], ref, line)
				}
			}
			if rec["msg"] == "http request" {
				if _, ok := rec["actor_ref"]; ok {
					t.Errorf("access log has actor_ref: %s", line)
				}
				if _, ok := rec["principal_uid"]; ok {
					t.Errorf("access log has principal_uid: %s", line)
				}
				if a, _ := rec["actor"].(string); a != "" {
					seen[a] = true
				}
			}
			if rec["msg"] == "http request actor" && level != slog.LevelDebug {
				t.Errorf("debug record at level %v: %s", level, line)
			}
		}
		for _, ref := range refs {
			if !seen[pseudonymizer.Pseudonym(ref)] {
				t.Errorf("level %v: no access log record with actor %s", level, pseudonymizer.Pseudonym(ref))
			}
		}
		if level == slog.LevelDebug && !strings.Contains(buf.String(), "alice.private@example.com") {
			t.Error("the debug record should carry the raw reference")
		}
	}
}
