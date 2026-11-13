package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	srv := newServer(t, api.ReadinessCheck{Name: "db", Code: api.CodeDBUnavailable})
	resp, b := do(t, srv, "GET", "/readyz", "", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"db":"not_configured"`) {
		t.Fatalf("readyz: %d %s", resp.StatusCode, b)
	}
	srv = newServer(t, api.ReadinessCheck{Name: "db", Code: api.CodeDBUnavailable, Check: func(context.Context) error { return errors.New("down") }})
	resp, b = do(t, srv, "GET", "/readyz", "", nil)
	if resp.StatusCode != 503 || decodeEnvelope(t, resp, b).Code != api.CodeDBUnavailable {
		t.Fatalf("readyz down: %d %s", resp.StatusCode, b)
	}
	srv = newServer(t, api.ReadinessCheck{Name: "db", Code: api.CodeDBUnavailable, Check: func(context.Context) error { return nil }})
	if resp, b = do(t, srv, "GET", "/readyz", "", nil); resp.StatusCode != 200 || !strings.Contains(string(b), `"db":"ok"`) {
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
	// Scheme is case-insensitive.
	resp, _ := do(t, srv, "GET", "/api/v1/workspaces/"+ulid1, "", map[string]string{"Authorization": "bearer " + token})
	if resp.StatusCode != 501 {
		t.Fatalf("lowercase scheme: %d", resp.StatusCode)
	}
}

// TestEveryOperationIsRoutedAndAnswers501 sends a valid request to every
// operation of the contract (except health) and expects the 501 envelope.
func TestEveryOperationIsRoutedAndAnswers501(t *testing.T) {
	srv := newServer(t)
	commit := `{"branch_id":"` + ulid1 + `","expected_generation":1,"message":"m","changes":[{"path":"a.txt","hash":"` + hashA + `"}]}`
	cases := []struct{ op, method, path, body, ctype string }{
		{"createProject", "POST", "/api/v1/projects", `{"name":"demo"}`, "application/json"},
		{"getProject", "GET", "/api/v1/projects/" + ulid1, "", ""},
		{"createWorkspace", "POST", "/api/v1/workspaces", `{"project_id":"` + ulid1 + `"}`, "application/json"},
		{"getWorkspace", "GET", "/api/v1/workspaces/" + ulid1, "", ""},
		{"listBranches", "GET", "/api/v1/workspaces/" + ulid1 + "/branches?limit=10", "", ""},
		{"putObject", "PUT", "/api/v1/workspaces/" + ulid1 + "/objects", "hello\n", "application/octet-stream"},
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
		if resp.StatusCode != http.StatusNotImplemented {
			t.Errorf("%s: status %d, body %s", c.op, resp.StatusCode, b)
			continue
		}
		e := decodeEnvelope(t, resp, b)
		if e.Code != api.CodeNotImplemented || e.Details["operation"] != c.op {
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
