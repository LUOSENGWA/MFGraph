package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/api"
	"github.com/LUOSENGWA/MFGraph/internal/authz"
	"github.com/LUOSENGWA/MFGraph/internal/core"
	"github.com/LUOSENGWA/MFGraph/internal/ids"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/ports/memory"
	"github.com/LUOSENGWA/MFGraph/internal/treestore"
)

const otherToken = "test-token-other-bbbbbbbbbbbb"

// newServiceServer serves the API with a core service on the in-memory
// ports, below prefix.
func newServiceServer(t *testing.T, prefix string, cfg core.Config) *httptest.Server {
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
	clock := memory.NewClock(time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC))
	cfg.TenantID = "00000000000000000000000000"
	svc, err := core.New(cfg, core.Deps{Store: store, Objects: objs, Trees: trees, Authz: authz.ProjectOwner{}, Clock: clock,
		IDs: memory.NewIDGen(clock)})
	if err != nil {
		t.Fatal(err)
	}
	authn, err := api.NewStaticTokens(map[string]string{token: uidA, otherToken: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.NewHandler(api.Options{Authenticator: authn, IDGen: ids.NewGenerator(), Service: svc,
		APIPrefix: prefix, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}))
	t.Cleanup(srv.Close)
	return srv
}

type caller struct {
	t      *testing.T
	srv    *httptest.Server
	prefix string
	token  string
}

func (c caller) call(method, path, body string, hdr map[string]string) (*http.Response, []byte) {
	c.t.Helper()
	h := map[string]string{"Authorization": "Bearer " + c.token}
	if body != "" && method != "PUT" {
		h["Content-Type"] = "application/json"
	}
	for k, v := range hdr {
		h[k] = v
	}
	return do(c.t, c.srv, method, c.prefix+path, body, h)
}

func (c caller) json(method, path, body string, want int, out any) {
	c.t.Helper()
	resp, b := c.call(method, path, body, nil)
	if resp.StatusCode != want {
		c.t.Fatalf("%s %s: %d %s, want %d", method, path, resp.StatusCode, b, want)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			c.t.Fatalf("%s %s: %v", method, path, err)
		}
	}
}

func (c caller) wantError(method, path, body string, hdr map[string]string, status int, code string) envelope {
	c.t.Helper()
	resp, b := c.call(method, path, body, hdr)
	e := decodeEnvelope(c.t, resp, b)
	if resp.StatusCode != status || e.Code != code {
		c.t.Fatalf("%s %s: %d %s, want %d %s", method, path, resp.StatusCode, b, status, code)
	}
	return e
}

func shaOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestHTTPFlowOnTheService(t *testing.T) {
	const prefix = "/internal/v1/files"
	srv := newServiceServer(t, prefix, core.Config{MaxUploadBytes: 64})
	c := caller{t: t, srv: srv, prefix: prefix, token: token}

	// The prefix replaces /api/v1; health stays at the root.
	if resp, _ := do(t, srv, "GET", "/api/v1/projects/"+ulid1, "", auth); resp.StatusCode != 404 {
		t.Fatalf("default prefix with a custom one: %d", resp.StatusCode)
	}
	if resp, _ := do(t, srv, "GET", "/healthz", "", nil); resp.StatusCode != 200 {
		t.Fatalf("healthz: %d", resp.StatusCode)
	}

	var proj struct {
		ProjectID string `json:"project_id"`
	}
	c.json("POST", "/projects", `{"name":"demo"}`, 201, &proj)
	var ws struct {
		ID   string `json:"workspace_id"`
		Main string `json:"main_branch_id"`
	}
	c.json("POST", "/workspaces", `{"project_id":"`+proj.ProjectID+`"}`, 201, &ws)

	// Uploads: 201, then 200; checksum mismatch; too large.
	upload := func(content string, want int) {
		t.Helper()
		resp, b := c.call("PUT", "/workspaces/"+ws.ID+"/objects", content,
			map[string]string{"Content-Type": "application/octet-stream", "X-Content-SHA256": shaOf(content)})
		if resp.StatusCode != want || !strings.Contains(string(b), `"status":"staged"`) {
			t.Fatalf("upload %q: %d %s", content, resp.StatusCode, b)
		}
	}
	upload("hello\n", 201)
	upload("hello\n", 200)
	upload("tool\n", 201)
	e := c.wantError("PUT", "/workspaces/"+ws.ID+"/objects", "x", map[string]string{"X-Content-SHA256": shaOf("y")}, 400, api.CodeInvalidRequest)
	if e.Details["reason"] != "checksum_mismatch" {
		t.Fatalf("checksum mismatch details: %+v", e)
	}
	e = c.wantError("PUT", "/workspaces/"+ws.ID+"/objects", strings.Repeat("x", 65), nil, 413, api.CodeFileTooLarge)
	if e.Details["max_bytes"] != float64(64) {
		t.Fatalf("too large details: %+v", e)
	}

	commit := func(gen int, changes string) string {
		return `{"branch_id":"` + ws.Main + `","expected_generation":` + string(rune('0'+gen)) + `,"changes":[` + changes + `]}`
	}
	ch := func(path, content string) string { return `{"path":"` + path + `","hash":"` + shaOf(content) + `"}` }
	var first struct {
		SnapshotID string `json:"snapshot_id"`
		Generation int    `json:"generation"`
	}
	c.json("POST", "/workspaces/"+ws.ID+"/commits", commit(1, ch("a.txt", "hello\n")+`,{"path":"bin/tool","hash":"`+shaOf("tool\n")+`","mode":493}`), 201, &first)
	if first.Generation != 2 {
		t.Fatalf("first commit: %+v", first)
	}
	e = c.wantError("POST", "/workspaces/"+ws.ID+"/commits", commit(1, ch("b.txt", "hello\n")), nil, 409, api.CodeConflict)
	if e.Details["current_generation"] != float64(2) {
		t.Fatalf("conflict details: %+v", e)
	}
	e = c.wantError("POST", "/workspaces/"+ws.ID+"/commits", commit(2, ch("a//b", "hello\n")), nil, 422, api.CodeInvalidPath)
	if e.Details["kind"] != "empty_segment" {
		t.Fatalf("invalid path details: %+v", e)
	}
	e = c.wantError("POST", "/workspaces/"+ws.ID+"/commits", commit(2, ch("c.txt", "never uploaded")), nil, 410, api.CodeBlobMissing)
	if e.Details["reason"] != "not_uploaded" {
		t.Fatalf("blob missing details: %+v", e)
	}
	e = c.wantError("POST", "/workspaces/"+ws.ID+"/commits", commit(2, `{"path":"nope","delete":true}`), nil, 409, api.CodeConflict)
	if e.Details["reason"] != "path_not_found" {
		t.Fatalf("delete details: %+v", e)
	}
	c.wantError("POST", "/workspaces/"+ws.ID+"/commits", commit(2, ch("a.txt", "hello\n")), nil, 400, api.CodeInvalidRequest)

	// Idempotent retry.
	body := commit(2, ch("docs/c.txt", "hello\n"))
	resp1, b1 := c.call("POST", "/workspaces/"+ws.ID+"/commits", body, map[string]string{"Idempotency-Key": "k-1"})
	resp2, b2 := c.call("POST", "/workspaces/"+ws.ID+"/commits", body, map[string]string{"Idempotency-Key": "k-1"})
	if resp1.StatusCode != 201 || resp2.StatusCode != 201 || string(b1) != string(b2) {
		t.Fatalf("idempotent retry: %d %s / %d %s", resp1.StatusCode, b1, resp2.StatusCode, b2)
	}
	c.wantError("POST", "/workspaces/"+ws.ID+"/commits", commit(3, ch("docs/d.txt", "hello\n")),
		map[string]string{"Idempotency-Key": "k-1"}, 409, api.CodeIdempotencyKeyReuse)
	var second struct {
		SnapshotID string `json:"snapshot_id"`
	}
	_ = json.Unmarshal(b1, &second)

	// Browse, download (ETag, 304, ranges, 416), diff, timeline.
	var listing struct {
		Items []struct {
			Name string `json:"name"`
			Type string `json:"type"`
			Mode *int   `json:"mode"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	c.json("GET", "/snapshots/"+second.SnapshotID+"/tree?limit=2", "", 200, &listing)
	if len(listing.Items) != 2 || listing.Items[0].Name != "a.txt" || listing.NextCursor == nil {
		t.Fatalf("listing: %+v", listing)
	}
	c.json("GET", "/snapshots/"+second.SnapshotID+"/tree?path=bin", "", 200, &listing)
	if len(listing.Items) != 1 || listing.Items[0].Mode == nil || *listing.Items[0].Mode != 493 {
		t.Fatalf("bin listing: %+v", listing)
	}
	c.wantError("GET", "/snapshots/"+second.SnapshotID+"/tree?path=a.txt", "", nil, 400, api.CodeInvalidRequest)
	c.wantError("GET", "/snapshots/"+second.SnapshotID+"/tree?path=missing", "", nil, 404, api.CodeNotFound)
	c.wantError("GET", "/snapshots/"+second.SnapshotID+"/tree?limit=0", "", nil, 400, api.CodeInvalidRequest)
	file := "/snapshots/" + second.SnapshotID + "/file?path="
	resp, b := c.call("GET", file+"a.txt", "", nil)
	etag := resp.Header.Get("ETag")
	if resp.StatusCode != 200 || string(b) != "hello\n" || etag != `"`+shaOf("hello\n")+`"` || resp.Header.Get("Accept-Ranges") != "bytes" ||
		resp.Header.Get("Content-Length") != "6" {
		t.Fatalf("download: %d %q %v", resp.StatusCode, b, resp.Header)
	}
	if resp, _ := c.call("GET", file+"a.txt", "", map[string]string{"If-None-Match": etag}); resp.StatusCode != 304 || resp.Header.Get("ETag") != etag {
		t.Fatalf("If-None-Match: %d", resp.StatusCode)
	}
	resp, b = c.call("GET", file+"a.txt", "", map[string]string{"Range": "bytes=1-3"})
	if resp.StatusCode != 206 || string(b) != "ell" || resp.Header.Get("Content-Range") != "bytes 1-3/6" {
		t.Fatalf("range: %d %q %v", resp.StatusCode, b, resp.Header)
	}
	resp, b = c.call("GET", file+"a.txt", "", map[string]string{"Range": "bytes=0-1,3-4"})
	if resp.StatusCode != 200 || string(b) != "hello\n" {
		t.Fatalf("multiple ranges are ignored: %d %q", resp.StatusCode, b)
	}
	resp, b = c.call("GET", file+"a.txt", "", map[string]string{"Range": "bytes=6-"})
	if resp.StatusCode != 416 || resp.Header.Get("Content-Range") != "bytes */6" || decodeEnvelope(t, resp, b).Code != api.CodeInvalidRequest {
		t.Fatalf("unsatisfiable range: %d %s %v", resp.StatusCode, b, resp.Header)
	}
	c.wantError("GET", file+"bin", "", nil, 400, api.CodeInvalidRequest)
	c.wantError("GET", file+url.QueryEscape("../x"), "", nil, 422, api.CodeInvalidPath)
	var diff struct {
		Base  *string `json:"base_snapshot_id"`
		Items []struct {
			Path   string `json:"path"`
			Change string `json:"change"`
		} `json:"items"`
	}
	c.json("GET", "/snapshots/"+second.SnapshotID+"/diff", "", 200, &diff)
	if diff.Base == nil || *diff.Base != first.SnapshotID || len(diff.Items) != 1 || diff.Items[0].Path != "docs/c.txt" || diff.Items[0].Change != "added" {
		t.Fatalf("diff: %+v", diff)
	}
	var timeline struct {
		Items []struct {
			ID          string `json:"snapshot_id"`
			TriggerKind string `json:"trigger_kind"`
			AuthorRef   string `json:"author_ref"`
			AuthorUID   string `json:"author_principal_uid"`
		} `json:"items"`
	}
	c.json("GET", "/workspaces/"+ws.ID+"/timeline?branch_id="+ws.Main, "", 200, &timeline)
	if len(timeline.Items) != 2 || timeline.Items[0].ID != second.SnapshotID || timeline.Items[0].TriggerKind != ports.TriggerManual ||
		timeline.Items[0].AuthorRef != uidA || timeline.Items[0].AuthorUID != uidA {
		t.Fatalf("timeline: %+v", timeline)
	}

	// Restore one path and the whole snapshot.
	var restored struct {
		SnapshotID   string `json:"snapshot_id"`
		RestoredFrom string `json:"restored_from"`
		Generation   int    `json:"generation"`
	}
	c.json("POST", "/snapshots/"+first.SnapshotID+"/restore", `{"branch_id":"`+ws.Main+`","expected_generation":3,"path":"docs"}`, 201, &restored)
	if restored.RestoredFrom != first.SnapshotID || restored.Generation != 4 {
		t.Fatalf("restore: %+v", restored)
	}
	c.wantError("GET", "/snapshots/"+restored.SnapshotID+"/file?path=docs/c.txt", "", nil, 404, api.CodeNotFound)

	// Another principal sees nothing.
	other := caller{t: t, srv: srv, prefix: prefix, token: otherToken}
	for _, path := range []string{"/projects/" + proj.ProjectID, "/workspaces/" + ws.ID, "/snapshots/" + first.SnapshotID,
		"/snapshots/" + first.SnapshotID + "/tree", "/snapshots/" + first.SnapshotID + "/file?path=a.txt"} {
		other.wantError("GET", path, "", nil, 404, api.CodeNotFound)
	}
	c.wantError("GET", "/snapshots/not-a-ulid", "", nil, 400, api.CodeInvalidRequest)
}
