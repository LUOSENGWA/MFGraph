//go:build e2e

package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sync"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/api/gen"
)

// ---- typed calls -----------------------------------------------------------

func (c *client) createProject(name string) gen.Project {
	c.t.Helper()
	var p gen.Project
	c.expect(c.json("POST", "/api/v1/projects", gen.CreateProjectRequest{Name: name}, nil), "POST /api/v1/projects", http.StatusCreated, &p)
	return p
}

func (c *client) createWorkspace(projectID string) gen.Workspace {
	c.t.Helper()
	var w gen.Workspace
	c.expect(c.json("POST", "/api/v1/workspaces", gen.CreateWorkspaceRequest{ProjectId: projectID}, nil), "POST /api/v1/workspaces", http.StatusCreated, &w)
	return w
}

func (c *client) mainBranch(ws gen.Workspace) gen.Branch {
	c.t.Helper()
	var l gen.BranchList
	c.expect(c.json("GET", "/api/v1/workspaces/"+ws.WorkspaceId+"/branches", nil, nil), "GET branches", http.StatusOK, &l)
	for _, b := range l.Items {
		if b.BranchId == ws.MainBranchId {
			return b
		}
	}
	c.t.Fatalf("main branch %s not listed", ws.MainBranchId)
	return gen.Branch{}
}

func sha(content string) string {
	s := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(s[:])
}

func (c *client) upload(wsID, content string) string {
	c.t.Helper()
	var o gen.StagedObject
	r := c.do("PUT", "/api/v1/workspaces/"+wsID+"/objects", bytes.NewReader([]byte(content)),
		map[string]string{"Content-Type": "application/octet-stream", "X-Content-SHA256": sha(content)})
	if r.status == http.StatusOK {
		c.expect(r, "PUT objects", http.StatusOK, &o)
	} else {
		c.expect(r, "PUT objects", http.StatusCreated, &o)
	}
	if o.Hash != sha(content) || o.Size != int64(len(content)) {
		c.t.Fatalf("upload returned %+v, want hash %s size %d", o, sha(content), len(content))
	}
	return o.Hash
}

func put(path, hash string) gen.Change { return gen.Change{Path: path, Hash: &hash} }

func (c *client) commitRaw(wsID string, req gen.CommitRequest, idemKey string) response {
	c.t.Helper()
	hdr := map[string]string{}
	if idemKey != "" {
		hdr["Idempotency-Key"] = idemKey
	}
	return c.json("POST", "/api/v1/workspaces/"+wsID+"/commits", req, hdr)
}

func (c *client) commit(wsID, branchID string, gen0 int64, changes ...gen.Change) gen.CommitResult {
	c.t.Helper()
	var res gen.CommitResult
	msg := "e2e commit"
	r := c.commitRaw(wsID, gen.CommitRequest{BranchId: branchID, ExpectedGeneration: gen0, Message: &msg, Changes: changes}, "")
	c.expect(r, "POST commits", http.StatusCreated, &res)
	return res
}

func (c *client) tree(snapshotID, dir string) gen.TreeListing {
	c.t.Helper()
	var l gen.TreeListing
	c.expect(c.json("GET", "/api/v1/snapshots/"+snapshotID+"/tree?path="+url.QueryEscape(dir), nil, nil), "GET tree "+dir, http.StatusOK, &l)
	return l
}

func (c *client) file(snapshotID, path string, hdr map[string]string) response {
	c.t.Helper()
	return c.do("GET", "/api/v1/snapshots/"+snapshotID+"/file?path="+url.QueryEscape(path), nil, hdr)
}

func (c *client) snapshot(id string) gen.Snapshot {
	c.t.Helper()
	var s gen.Snapshot
	c.expect(c.json("GET", "/api/v1/snapshots/"+id, nil, nil), "GET snapshot", http.StatusOK, &s)
	return s
}

// seeded is a workspace with one commit of two files.
type seeded struct {
	ws     gen.Workspace
	branch gen.Branch
	first  gen.CommitResult
}

const (
	contentA = "hello\n"
	contentB = "second file\n"
)

func seed(t *testing.T, c *client) seeded {
	t.Helper()
	p := c.createProject("e2e project")
	ws := c.createWorkspace(p.ProjectId)
	br := c.mainBranch(ws)
	if br.RefName != "main" || br.HeadSnapshotId != nil {
		t.Fatalf("new main branch: %+v", br)
	}
	ha, hb := c.upload(ws.WorkspaceId, contentA), c.upload(ws.WorkspaceId, contentB)
	first := c.commit(ws.WorkspaceId, br.BranchId, br.Generation, put("a.txt", ha), put("docs/b.txt", hb))
	return seeded{ws: ws, branch: br, first: first}
}

// ---- acceptance cases ------------------------------------------------------

// 1. project -> workspace -> upload 2 files -> commit -> browse -> download.
func TestUploadCommitBrowseDownload(t *testing.T) {
	s := start(t, newEnv(t))
	c := s.client(tokenA)
	sd := seed(t, c)

	root := c.tree(sd.first.SnapshotId, "")
	if len(root.Items) != 2 || root.Items[0].Name != "a.txt" || root.Items[0].Type != gen.File ||
		root.Items[1].Name != "docs" || root.Items[1].Type != gen.Dir {
		t.Fatalf("root listing: %+v", root.Items)
	}
	docs := c.tree(sd.first.SnapshotId, "docs")
	if len(docs.Items) != 1 || docs.Items[0].Path != "docs/b.txt" {
		t.Fatalf("docs listing: %+v", docs.Items)
	}
	for path, want := range map[string]string{"a.txt": contentA, "docs/b.txt": contentB} {
		r := c.file(sd.first.SnapshotId, path, nil)
		c.expect(r, "GET file "+path, http.StatusOK, nil)
		if string(r.body) != want {
			t.Fatalf("%s: downloaded %q, want %q", path, r.body, want)
		}
	}
	r := c.file(sd.first.SnapshotId, "a.txt", map[string]string{"Range": "bytes=1-3"})
	c.expect(r, "GET file with Range", http.StatusPartialContent, nil)
	if string(r.body) != "ell" || r.header.Get("Content-Range") != "bytes 1-3/6" {
		t.Fatalf("range: %q %q", r.body, r.header.Get("Content-Range"))
	}
}

// 2. change one file and commit -> the diff shows only that file.
func TestDiffShowsOnlyTheChangedFile(t *testing.T) {
	s := start(t, newEnv(t))
	c := s.client(tokenA)
	sd := seed(t, c)
	h := c.upload(sd.ws.WorkspaceId, "hello, again\n")
	second := c.commit(sd.ws.WorkspaceId, sd.branch.BranchId, sd.first.Generation, put("a.txt", h))

	var d gen.Diff
	c.expect(c.json("GET", "/api/v1/snapshots/"+second.SnapshotId+"/diff?base="+sd.first.SnapshotId, nil, nil), "GET diff", http.StatusOK, &d)
	if len(d.Items) != 1 || d.Items[0].Path != "a.txt" || d.Items[0].Change != gen.Modified {
		t.Fatalf("diff items: %+v", d.Items)
	}
	if d.Items[0].Old == nil || d.Items[0].Old.Hash != sha(contentA) || d.Items[0].New == nil || d.Items[0].New.Hash != h {
		t.Fatalf("diff versions: %+v", d.Items[0])
	}
}

// 3. restore the first snapshot -> a new snapshot with the first tree.
func TestRestoreCreatesNewSnapshot(t *testing.T) {
	s := start(t, newEnv(t))
	c := s.client(tokenA)
	sd := seed(t, c)
	h := c.upload(sd.ws.WorkspaceId, "changed\n")
	second := c.commit(sd.ws.WorkspaceId, sd.branch.BranchId, sd.first.Generation, put("a.txt", h))

	var res gen.CommitResult
	msg := "restore first"
	c.expect(c.json("POST", "/api/v1/snapshots/"+sd.first.SnapshotId+"/restore",
		gen.RestoreRequest{BranchId: sd.branch.BranchId, ExpectedGeneration: second.Generation, Message: &msg}, nil),
		"POST restore", http.StatusCreated, &res)
	if res.SnapshotId == sd.first.SnapshotId || res.SnapshotId == second.SnapshotId {
		t.Fatal("restore must create a new snapshot")
	}
	if res.RootTreeHash != sd.first.RootTreeHash {
		t.Fatalf("restored tree %s, want %s", res.RootTreeHash, sd.first.RootTreeHash)
	}
	if len(res.ParentIds) != 1 || res.ParentIds[0] != second.SnapshotId {
		t.Fatalf("restored parent %v, want [%s]", res.ParentIds, second.SnapshotId)
	}
	if got := c.snapshot(res.SnapshotId); got.TriggerKind != "restore" {
		t.Fatalf("trigger_kind %q", got.TriggerKind)
	}
	r := c.file(res.SnapshotId, "a.txt", nil)
	c.expect(r, "GET restored file", http.StatusOK, nil)
	if string(r.body) != contentA {
		t.Fatalf("restored content %q", r.body)
	}
}

// 4. data survives a restart of the server.
func TestDataSurvivesRestart(t *testing.T) {
	e := newEnv(t)
	s := start(t, e)
	sd := seed(t, s.client(tokenA))
	s.stop()

	s2 := start(t, e)
	c := s2.client(tokenA)
	if got := c.snapshot(sd.first.SnapshotId); got.RootTreeHash != sd.first.RootTreeHash {
		t.Fatalf("after restart: %+v", got)
	}
	if br := c.mainBranch(sd.ws); br.HeadSnapshotId == nil || *br.HeadSnapshotId != sd.first.SnapshotId {
		t.Fatalf("branch head after restart: %+v", br)
	}
	r := c.file(sd.first.SnapshotId, "docs/b.txt", nil)
	c.expect(r, "GET file after restart", http.StatusOK, nil)
	if string(r.body) != contentB {
		t.Fatalf("content after restart %q", r.body)
	}
}

// 5. two commits with the same expected_generation -> one 201, one 409.
func TestConcurrentCommitsOneWins(t *testing.T) {
	s := start(t, newEnv(t))
	c := s.client(tokenA)
	sd := seed(t, c)
	h1, h2 := c.upload(sd.ws.WorkspaceId, "writer one\n"), c.upload(sd.ws.WorkspaceId, "writer two\n")
	statuses := make([]response, 2)
	var wg sync.WaitGroup
	for i, h := range []string{h1, h2} {
		wg.Add(1)
		go func(i int, h string) {
			defer wg.Done()
			statuses[i] = c.commitRaw(sd.ws.WorkspaceId, gen.CommitRequest{BranchId: sd.branch.BranchId,
				ExpectedGeneration: sd.first.Generation, Changes: []gen.Change{put("a.txt", h)}}, "")
		}(i, h)
	}
	wg.Wait()
	created, conflict := 0, 0
	for _, r := range statuses {
		switch {
		case r.status == http.StatusCreated:
			created++
		case r.status == http.StatusConflict && r.code() == "conflict":
			conflict++
		default:
			t.Fatalf("concurrent commit: got %d %s; body: %s", r.status, r.code(), truncate(r.body))
		}
	}
	if created != 1 || conflict != 1 {
		t.Fatalf("created=%d conflict=%d, want 1 and 1", created, conflict)
	}
}

// A restore and a commit with the same expected_generation: one 201, one
// 409 conflict, and the branch moves once.
func TestRestoreAndCommitRace(t *testing.T) {
	s := start(t, newEnv(t))
	c := s.client(tokenA)
	sd := seed(t, c)
	second := c.commit(sd.ws.WorkspaceId, sd.branch.BranchId, sd.first.Generation, put("a.txt", c.upload(sd.ws.WorkspaceId, "changed\n")))
	h := c.upload(sd.ws.WorkspaceId, "racing commit\n")
	results := make([]response, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		results[0] = c.json("POST", "/api/v1/snapshots/"+sd.first.SnapshotId+"/restore",
			gen.RestoreRequest{BranchId: sd.branch.BranchId, ExpectedGeneration: second.Generation}, nil)
	}()
	go func() {
		defer wg.Done()
		results[1] = c.commitRaw(sd.ws.WorkspaceId, gen.CommitRequest{BranchId: sd.branch.BranchId,
			ExpectedGeneration: second.Generation, Changes: []gen.Change{put("race.txt", h)}}, "")
	}()
	wg.Wait()
	var winner gen.CommitResult
	created, conflict := 0, 0
	for i, r := range results {
		switch {
		case r.status == http.StatusCreated:
			created++
			c.expect(r, "the request that won", http.StatusCreated, &winner)
		case r.status == http.StatusConflict && r.code() == "conflict":
			conflict++
		default:
			t.Fatalf("request %d: got %d %s; body: %s", i, r.status, r.code(), truncate(r.body))
		}
	}
	if created != 1 || conflict != 1 {
		t.Fatalf("created=%d conflict=%d, want 1 and 1", created, conflict)
	}
	br := c.mainBranch(sd.ws)
	if br.Generation != second.Generation+1 || br.HeadSnapshotId == nil || *br.HeadSnapshotId != winner.SnapshotId {
		t.Fatalf("branch %+v, want generation %d at %s", br, second.Generation+1, winner.SnapshotId)
	}
}

// 6. retrying with the same Idempotency-Key returns the same snapshot.
func TestIdempotentCommitRetry(t *testing.T) {
	s := start(t, newEnv(t))
	c := s.client(tokenA)
	sd := seed(t, c)
	h := c.upload(sd.ws.WorkspaceId, "idempotent\n")
	req := gen.CommitRequest{BranchId: sd.branch.BranchId, ExpectedGeneration: sd.first.Generation, Changes: []gen.Change{put("c.txt", h)}}
	var first, retry gen.CommitResult
	c.expect(c.commitRaw(sd.ws.WorkspaceId, req, "retry-key-1"), "POST commits (first)", http.StatusCreated, &first)
	c.expect(c.commitRaw(sd.ws.WorkspaceId, req, "retry-key-1"), "POST commits (retry)", http.StatusCreated, &retry)
	if retry.SnapshotId != first.SnapshotId || retry.Generation != first.Generation {
		t.Fatalf("retry returned %s/%d, want %s/%d", retry.SnapshotId, retry.Generation, first.SnapshotId, first.Generation)
	}
	req.Changes = []gen.Change{put("d.txt", h)}
	r := c.commitRaw(sd.ws.WorkspaceId, req, "retry-key-1")
	if r.status != http.StatusConflict || r.code() != "idempotency_key_reuse" {
		t.Fatalf("same key, different body: got %d %s, want 409 idempotency_key_reuse", r.status, r.code())
	}
}

// 7. an invalid path is rejected with 422 invalid_path.
func TestInvalidPathRejected(t *testing.T) {
	s := start(t, newEnv(t))
	c := s.client(tokenA)
	sd := seed(t, c)
	h := c.upload(sd.ws.WorkspaceId, "evil\n")
	for _, p := range []string{"../evil", "a//b", "/abs", `back\slash`} {
		r := c.commitRaw(sd.ws.WorkspaceId, gen.CommitRequest{BranchId: sd.branch.BranchId,
			ExpectedGeneration: sd.first.Generation, Changes: []gen.Change{put(p, h)}}, "")
		if r.status != http.StatusUnprocessableEntity || r.code() != "invalid_path" {
			t.Fatalf("path %q: got %d %s, want 422 invalid_path; body: %s", p, r.status, r.code(), truncate(r.body))
		}
	}
}

// 8. the token of principal A cannot see the snapshots of principal B's
// workspace: 404, not 403.
func TestCrossWorkspaceIsNotFound(t *testing.T) {
	s := start(t, newEnv(t))
	owner := seed(t, s.client(tokenA))
	other := s.client(tokenB)
	otherProject := other.createProject("other project")
	other.createWorkspace(otherProject.ProjectId)

	for _, path := range []string{
		"/api/v1/snapshots/" + owner.first.SnapshotId,
		"/api/v1/snapshots/" + owner.first.SnapshotId + "/tree",
		"/api/v1/snapshots/" + owner.first.SnapshotId + "/file?path=a.txt",
		"/api/v1/workspaces/" + owner.ws.WorkspaceId,
	} {
		r := other.json("GET", path, nil, nil)
		if r.status != http.StatusNotFound || r.code() != "not_found" {
			t.Fatalf("GET %s as another principal: got %d %s, want 404 not_found", path, r.status, r.code())
		}
	}
}

// 9. A change without mode keeps the mode of an existing file (493 here) and
// gives new files 420; an explicit mode always wins.
func TestModeKeptWhenOmitted(t *testing.T) {
	s := start(t, newEnv(t))
	c := s.client(tokenA)
	sd := seed(t, c)
	ws, br := sd.ws.WorkspaceId, sd.branch.BranchId

	h1 := c.upload(ws, "#!/bin/sh\necho one\n")
	exec := gen.N493
	first := c.commit(ws, br, sd.first.Generation, gen.Change{Path: "tool.sh", Hash: &h1, Mode: &exec})

	h2 := c.upload(ws, "#!/bin/sh\necho two\n")
	second := c.commit(ws, br, first.Generation, put("tool.sh", h2), put("new.txt", h2))

	modes := map[string]gen.FileMode{}
	for _, e := range c.tree(second.SnapshotId, "").Items {
		if e.Mode != nil {
			modes[e.Name] = *e.Mode
		}
	}
	if modes["tool.sh"] != gen.N493 {
		t.Fatalf("tool.sh replaced without mode: mode %d, want 493 (kept)", modes["tool.sh"])
	}
	if modes["new.txt"] != gen.N420 || modes["a.txt"] != gen.N420 {
		t.Fatalf("files added without mode: new.txt %d, a.txt %d, want 420", modes["new.txt"], modes["a.txt"])
	}

	regular := gen.N420
	third := c.commit(ws, br, second.Generation, gen.Change{Path: "tool.sh", Hash: &h2, Mode: &regular})
	for _, e := range c.tree(third.SnapshotId, "").Items {
		if e.Name == "tool.sh" && (e.Mode == nil || *e.Mode != gen.N420) {
			t.Fatalf("explicit mode 420 must win: %+v", e)
		}
	}
}
