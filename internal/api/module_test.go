package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/api"
	"github.com/LUOSENGWA/MFGraph/internal/authz"
	"github.com/LUOSENGWA/MFGraph/internal/core"
	"github.com/LUOSENGWA/MFGraph/internal/ids"
	"github.com/LUOSENGWA/MFGraph/internal/ports/memory"
	"github.com/LUOSENGWA/MFGraph/internal/treestore"
)

const (
	modulePrefix = "/internal/v1/mfgraph"
	projectQ     = "01JD3X5Q8Z9W2V4T6R8P0N2P0Q"
)

// newModuleServer serves the API as a module process does: identity
// headers, project containment, no project endpoints, the module prefix.
func newModuleServer(t *testing.T) *httptest.Server {
	t.Helper()
	store := memory.NewStore()
	objs := memory.NewBlobStore()
	trees, err := treestore.New(objs, func(ctx context.Context, ws string) (string, error) {
		w, err := store.GetWorkspace(ctx, nil, ws)
		return w.StoragePrefix, err
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := memory.NewClock(time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC))
	svc, err := core.New(core.Config{TenantID: "01JD3X5Q8Z9W2V4T6R8P0N2T00", HostOwnsProjects: true},
		core.Deps{Store: store, Objects: objs, Trees: trees, Authz: authz.ProjectScope{}, Clock: clock, IDs: memory.NewIDGen(clock)})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.NewHandler(api.Options{Authenticator: trustedT(t), IDGen: ids.NewGenerator(), Service: svc,
		APIPrefix: modulePrefix, TrustedHeaderPrefix: prefixT, NoProjects: true,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}))
	t.Cleanup(srv.Close)
	return srv
}

// moduleCall sends a request with the identity headers of project.
func moduleCall(t *testing.T, srv *httptest.Server, project, method, path, body string, hdr ...string) (*http.Response, []byte) {
	t.Helper()
	h := claimsT(prefixT+"Project", project)
	if body != "" && method == "POST" {
		h["Content-Type"] = "application/json"
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		h[hdr[i]] = hdr[i+1]
	}
	return do(t, srv, method, modulePrefix+path, body, h)
}

// TestModuleHTTP: the module prefix, the missing project endpoints and the
// containment of every endpoint, over HTTP.
func TestModuleHTTP(t *testing.T) {
	srv := newModuleServer(t)
	mustJSON := func(resp *http.Response, b []byte, want int, out any) {
		t.Helper()
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d %s, want %d", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, b, want)
		}
		if out != nil {
			if err := json.Unmarshal(b, out); err != nil {
				t.Fatal(err)
			}
		}
	}
	var ws struct {
		ID   string `json:"workspace_id"`
		Main string `json:"main_branch_id"`
	}
	resp, b := moduleCall(t, srv, projectT, "POST", "/workspaces", `{"project_id":"`+projectT+`","display_name":"m1a"}`)
	mustJSON(resp, b, 201, &ws)
	resp, b = moduleCall(t, srv, projectT, "PUT", "/workspaces/"+ws.ID+"/objects", "hello", "Content-Type", "application/octet-stream")
	mustJSON(resp, b, 201, nil)
	var commit struct {
		SnapshotID string `json:"snapshot_id"`
	}
	resp, b = moduleCall(t, srv, projectT, "POST", "/workspaces/"+ws.ID+"/commits",
		`{"branch_id":"`+ws.Main+`","expected_generation":1,"changes":[{"path":"f.txt","hash":"`+shaOf("hello")+`"}]}`)
	mustJSON(resp, b, 201, &commit)
	var snap struct {
		AuthorRef  string `json:"author_ref"`
		AuthorType string `json:"author_type"`
	}
	resp, b = moduleCall(t, srv, projectT, "GET", "/snapshots/"+commit.SnapshotID, "")
	mustJSON(resp, b, 200, &snap)
	if snap.AuthorRef != "manager" {
		t.Fatalf("snapshot author: %s", b)
	}

	// Another project: every endpoint answers exactly like an unknown ID.
	const unknown = "01JD3X5Q8Z9W2V4T6R8P0N2MZZ"
	type req struct{ method, path, body string }
	cross := func(ws, snap, branch string) []req {
		return []req{
			{"GET", "/workspaces/" + ws, ""},
			{"GET", "/workspaces/" + ws + "/branches", ""},
			{"GET", "/workspaces/" + ws + "/timeline", ""},
			{"PUT", "/workspaces/" + ws + "/objects", "x"},
			{"POST", "/workspaces/" + ws + "/commits", `{"branch_id":"` + branch + `","expected_generation":2,"changes":[{"path":"f.txt","delete":true}]}`},
			{"GET", "/snapshots/" + snap, ""},
			{"GET", "/snapshots/" + snap + "/tree", ""},
			{"GET", "/snapshots/" + snap + "/file?path=f.txt", ""},
			{"GET", "/snapshots/" + snap + "/diff", ""},
			{"POST", "/snapshots/" + snap + "/restore", `{"branch_id":"` + branch + `","expected_generation":2}`},
		}
	}
	real, missing := cross(ws.ID, commit.SnapshotID, ws.Main), cross(unknown, unknown, ws.Main)
	for i := range real {
		r1, b1 := moduleCall(t, srv, projectQ, real[i].method, real[i].path, real[i].body)
		r2, b2 := moduleCall(t, srv, projectQ, missing[i].method, missing[i].path, missing[i].body)
		e1, e2 := decodeEnvelope(t, r1, b1), decodeEnvelope(t, r2, b2)
		e1.RequestID, e2.RequestID = "", ""
		if r1.StatusCode != 404 || r2.StatusCode != 404 || e1.Code != api.CodeNotFound || e1.Message != e2.Message ||
			len(e1.Details) != 0 || len(e2.Details) != 0 {
			t.Errorf("%s %s from another project: %d %+v; unknown ID: %d %+v", real[i].method, real[i].path, r1.StatusCode, e1,
				r2.StatusCode, e2)
		}
	}
	resp, b = moduleCall(t, srv, projectQ, "POST", "/workspaces", `{"project_id":"`+projectT+`"}`)
	if e := decodeEnvelope(t, resp, b); resp.StatusCode != 404 || e.Code != api.CodeNotFound {
		t.Fatalf("workspace in another project: %d %s", resp.StatusCode, b)
	}

	// No project endpoints, whatever the method or the body; the standard
	// prefix does not exist.
	for _, r := range []req{{"POST", "/projects", `{"name":"p"}`}, {"POST", "/projects", "not json"}, {"GET", "/projects/" + projectT, ""},
		{"GET", "/projects", ""}, {"DELETE", "/projects/" + projectT, ""}} {
		resp, b := moduleCall(t, srv, projectT, r.method, r.path, r.body)
		if e := decodeEnvelope(t, resp, b); resp.StatusCode != 404 || e.Code != api.CodeNotFound || e.Message != "no such endpoint" {
			t.Errorf("%s %s: %d %s", r.method, r.path, resp.StatusCode, b)
		}
	}
	resp, b = do(t, srv, "GET", "/api/v1/snapshots/"+commit.SnapshotID, "", claimsT())
	if e := decodeEnvelope(t, resp, b); resp.StatusCode != 404 || e.Code != api.CodeNotFound {
		t.Fatalf("/api/v1 in module mode: %d %s", resp.StatusCode, b)
	}
	// Without the identity headers: 401 before anything else.
	resp, b = do(t, srv, "GET", modulePrefix+"/projects", "", nil)
	if e := decodeEnvelope(t, resp, b); resp.StatusCode != 401 || e.Code != api.CodeUnauthenticated {
		t.Fatalf("unauthenticated: %d %s", resp.StatusCode, b)
	}
	if !strings.Contains(string(b), "internal token") {
		t.Fatalf("401 message: %s", b)
	}
}
