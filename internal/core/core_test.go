package core_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/authz"
	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/core"
	"github.com/LUOSENGWA/MFGraph/internal/pathx"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/ports/memory"
	"github.com/LUOSENGWA/MFGraph/internal/storagekey"
	"github.com/LUOSENGWA/MFGraph/internal/tree"
	"github.com/LUOSENGWA/MFGraph/internal/treestore"
)

var ctx = context.Background()

const tenant = "00000000000000000000000000"

var (
	alice = ports.Principal{Ref: "alice"}
	bob   = ports.Principal{Ref: "01JD3X5Q8Z9W2V4T6R8P0N2MB0", PrincipalUID: "01JD3X5Q8Z9W2V4T6R8P0N2MB0"}
)

type env struct {
	t     *testing.T
	svc   *core.Service
	store *memory.Store
	objs  *memory.BlobStore
	clock *memory.Clock
}

func newEnv(t *testing.T, cfg core.Config) *env {
	t.Helper()
	return newEnvStore(t, cfg, nil)
}

// newEnvStore is newEnv with the store the service uses wrapped by wrap
// (unless nil); env.store stays the in-memory store.
func newEnvStore(t *testing.T, cfg core.Config, wrap func(*memory.Store) ports.Store) *env {
	t.Helper()
	store := memory.NewStore()
	var svcStore ports.Store = store
	if wrap != nil {
		svcStore = wrap(store)
	}
	objs := memory.NewBlobStore()
	trees, err := treestore.New(objs, treestore.CachedPrefixes(func(ctx context.Context, ws string) (string, error) {
		w, err := store.GetWorkspace(ctx, nil, ws)
		return w.StoragePrefix, err
	}, 100))
	if err != nil {
		t.Fatal(err)
	}
	clock := memory.NewClock(time.Date(2026, 11, 15, 8, 0, 0, 0, time.UTC))
	cfg.TenantID = tenant
	svc, err := core.New(cfg, core.Deps{Store: svcStore, Objects: objs, Trees: trees, Authz: authz.ProjectOwner{}, Clock: clock,
		IDs: memory.NewIDGen(clock)})
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, svc: svc, store: store, objs: objs, clock: clock}
}

func (e *env) must(err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
}

// wantCode fails unless err has the code (and the details, if given).
func wantCode(t *testing.T, what string, err error, code string, details ...any) {
	t.Helper()
	if core.CodeOf(err) != code {
		t.Fatalf("%s: err = %v, want code %s", what, err, code)
	}
	var ce *core.Error
	if len(details) > 0 {
		if !errorAs(err, &ce) {
			t.Fatalf("%s: not a core error", what)
		}
		for i := 0; i+1 < len(details); i += 2 {
			k := details[i].(string)
			if got := fmt.Sprint(ce.Details[k]); got != fmt.Sprint(details[i+1]) {
				t.Fatalf("%s: details[%s] = %v, want %v", what, k, got, details[i+1])
			}
		}
	}
}

func errorAs(err error, target **core.Error) bool {
	for err != nil {
		if e, ok := err.(*core.Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func wantPathKind(t *testing.T, what string, err error, kind pathx.Kind) {
	t.Helper()
	if pathx.KindOf(err) != kind {
		t.Fatalf("%s: err = %v, want invalid path %s", what, err, kind)
	}
}

type seeded struct {
	ws     core.WorkspaceView
	branch string
	first  core.CommitResult
}

func (e *env) workspace(p ports.Principal) core.WorkspaceView {
	e.t.Helper()
	proj, err := e.svc.CreateProject(ctx, p, core.CreateProjectRequest{Name: "demo"}, "")
	e.must(err)
	ws, err := e.svc.CreateWorkspace(ctx, p, core.CreateWorkspaceRequest{ProjectID: proj.ID}, "")
	e.must(err)
	return ws
}

func (e *env) upload(p ports.Principal, ws, content string) string {
	e.t.Helper()
	b, _, err := e.svc.PutObject(ctx, p, core.PutObjectRequest{WorkspaceID: ws, Body: strings.NewReader(content), ContentLength: -1})
	e.must(err)
	return b.Hash.String()
}

func put(path, hash string) core.Change { return core.Change{Path: path, Hash: &hash} }

func del(path string) core.Change {
	t := true
	return core.Change{Path: path, Delete: &t}
}

func (e *env) commit(p ports.Principal, ws, branch string, gen int64, cs ...core.Change) core.CommitResult {
	e.t.Helper()
	r, err := e.svc.Commit(ctx, p, ws, core.CommitRequest{BranchID: branch, ExpectedGeneration: gen, Message: "m", Changes: cs}, "")
	e.must(err)
	return r
}

func (e *env) seed(p ports.Principal) seeded {
	e.t.Helper()
	ws := e.workspace(p)
	id := ws.Workspace.ID
	first := e.commit(p, id, ws.MainBranchID, 1, put("a.txt", e.upload(p, id, "hello\n")), put("docs/b.txt", e.upload(p, id, "second file\n")))
	return seeded{ws: ws, branch: ws.MainBranchID, first: first}
}

func (e *env) read(p ports.Principal, snap, path string) string {
	e.t.Helper()
	f, err := e.svc.GetFile(ctx, p, snap, path)
	e.must(err)
	rc, err := f.Open(ctx, 0, -1)
	e.must(err)
	defer rc.Close()
	b, err := io.ReadAll(rc)
	e.must(err)
	return string(b)
}

func TestUploadCommitBrowseDiffRestore(t *testing.T) {
	e := newEnv(t, core.Config{})
	sd := e.seed(alice)
	ws := sd.ws.Workspace.ID
	if sd.first.Generation != 2 || len(sd.first.ParentIDs) != 0 || sd.first.RefName != "main" {
		t.Fatalf("first commit: %+v", sd.first)
	}
	snap, err := e.svc.GetSnapshot(ctx, alice, sd.first.SnapshotID)
	e.must(err)
	if snap.TriggerKind != ports.TriggerManual || snap.EntryCount != 2 || snap.TotalBytes != 18 || snap.AuthorRef != "alice" {
		t.Fatalf("snapshot: %+v", snap)
	}
	root, err := e.svc.ListTree(ctx, alice, sd.first.SnapshotID, "", core.Page{})
	e.must(err)
	if len(root.Items) != 2 || root.Items[0].Path != "a.txt" || root.Items[1].Entry.Type != tree.TypeDir || root.NextCursor != "" {
		t.Fatalf("root listing: %+v", root)
	}
	docs, err := e.svc.ListTree(ctx, alice, sd.first.SnapshotID, "docs", core.Page{})
	e.must(err)
	if len(docs.Items) != 1 || docs.Items[0].Path != "docs/b.txt" || docs.Path != "docs" {
		t.Fatalf("docs listing: %+v", docs)
	}
	if got := e.read(alice, sd.first.SnapshotID, "docs/b.txt"); got != "second file\n" {
		t.Fatalf("download: %q", got)
	}
	f, err := e.svc.GetFile(ctx, alice, sd.first.SnapshotID, "a.txt")
	e.must(err)
	rc, err := f.Open(ctx, 1, 3)
	e.must(err)
	part, _ := io.ReadAll(rc)
	rc.Close()
	if string(part) != "ell" || f.ETag() != `"`+contenthash.SumBytes([]byte("hello\n")).String()+`"` || f.Entry.Size != 6 {
		t.Fatalf("range %q, etag %s", part, f.ETag())
	}

	// Change one file: the diff lists only it.
	h := e.upload(alice, ws, "hello, again\n")
	second := e.commit(alice, ws, sd.branch, 2, put("a.txt", h))
	d, err := e.svc.Diff(ctx, alice, second.SnapshotID, "", core.Page{})
	e.must(err)
	if d.BaseSnapshotID != sd.first.SnapshotID || len(d.Items) != 1 || d.Items[0].Path != "a.txt" || d.Items[0].Kind != tree.DiffModified {
		t.Fatalf("diff: %+v", d)
	}
	// The first snapshot against the empty tree: everything is added.
	d, err = e.svc.Diff(ctx, alice, sd.first.SnapshotID, "", core.Page{})
	e.must(err)
	if d.BaseSnapshotID != "" || len(d.Items) != 2 || d.Items[0].Kind != tree.DiffAdded {
		t.Fatalf("diff of the root snapshot: %+v", d)
	}

	// Restore the first snapshot: a new snapshot with the first tree.
	r, err := e.svc.Restore(ctx, alice, sd.first.SnapshotID, core.RestoreRequest{BranchID: sd.branch, ExpectedGeneration: 3}, "")
	e.must(err)
	if r.RootTreeHash != sd.first.RootTreeHash || r.RestoredFrom != sd.first.SnapshotID || len(r.ParentIDs) != 1 ||
		r.ParentIDs[0] != second.SnapshotID || r.Generation != 4 {
		t.Fatalf("restore: %+v", r)
	}
	rs, _ := e.svc.GetSnapshot(ctx, alice, r.SnapshotID)
	if rs.TriggerKind != ports.TriggerRestore || rs.EntryCount != 2 || rs.TotalBytes != 18 {
		t.Fatalf("restored snapshot: %+v", rs)
	}
	if got := e.read(alice, r.SnapshotID, "a.txt"); got != "hello\n" {
		t.Fatalf("restored content: %q", got)
	}
	// Restoring the same tree again changes nothing.
	_, err = e.svc.Restore(ctx, alice, sd.first.SnapshotID, core.RestoreRequest{BranchID: sd.branch, ExpectedGeneration: 4}, "")
	wantCode(t, "restore of the head's tree", err, core.CodeInvalidRequest, "reason", core.ReasonNoChanges)

	// Blobs became live; the outbox has the events of the three snapshots.
	b, _ := e.store.GetBlob(ctx, nil, ws, contenthash.MustParse(h))
	if b.Status != ports.BlobLive {
		t.Fatalf("blob status %s", b.Status)
	}
	events, _ := e.store.Events(ctx)
	var types []string
	for _, ev := range events {
		types = append(types, ev.Type)
		var payload map[string]any
		if err := json.Unmarshal(ev.Payload, &payload); err != nil || payload["schema_version"] != float64(1) || payload["workspace_id"] != ws {
			t.Fatalf("event payload %s: %v", ev.Payload, err)
		}
	}
	if strings.Join(types, ",") != "snapshot.created,branch.updated,snapshot.created,branch.updated,snapshot.restored,branch.updated" {
		t.Fatalf("events: %v", types)
	}
	if !e.store.HasUsage(ws) {
		t.Fatal("no usage row")
	}
}

func TestCommitErrors(t *testing.T) {
	e := newEnv(t, core.Config{MaxCommitChanges: 3})
	sd := e.seed(alice)
	ws, br := sd.ws.Workspace.ID, sd.branch
	h := e.upload(alice, ws, "x")
	commit := func(gen int64, cs ...core.Change) error {
		_, err := e.svc.Commit(ctx, alice, ws, core.CommitRequest{BranchID: br, ExpectedGeneration: gen, Changes: cs}, "")
		return err
	}
	wantCode(t, "stale generation", commit(1, put("x", h)), core.CodeConflict, "current_generation", 2)
	other := e.workspace(alice)
	_, err := e.svc.Commit(ctx, alice, ws, core.CommitRequest{BranchID: other.MainBranchID, ExpectedGeneration: 1,
		Changes: []core.Change{put("x", h)}}, "")
	wantCode(t, "branch of another workspace", err, core.CodeNotFound)
	_, err = e.svc.Commit(ctx, alice, ws, core.CommitRequest{BranchID: "01JD3X5Q8Z9W2V4T6R8P0N2MZZ", ExpectedGeneration: 1,
		Changes: []core.Change{put("x", h)}}, "")
	wantCode(t, "unknown branch", err, core.CodeNotFound)
	notUploaded := contenthash.SumBytes([]byte("never uploaded")).String()
	wantCode(t, "content not uploaded", commit(2, put("x", h), put("y", notUploaded)), core.CodeBlobMissing,
		"reason", core.ReasonNotUploaded, "hashes", []string{notUploaded})
	// Content that is recorded but lost from storage.
	lost := e.upload(alice, ws, "lost")
	e.must(e.objs.Delete(ctx, storagekey.Join(sd.ws.Workspace.StoragePrefix, storagekey.ObjectKey(contenthash.MustParse(lost)))))
	wantCode(t, "content missing in storage", commit(2, put("y", lost)), core.CodeBlobMissing, "reason", core.ReasonMissingInStorage)
	wantCode(t, "delete of a missing path", commit(2, del("nope.txt")), core.CodeConflict, "reason", core.ReasonPathNotFound)
	wantCode(t, "delete of a directory", commit(2, del("docs")), core.CodeConflict, "reason", core.ReasonPathNotFound)
	wantPathKind(t, "dot segment", commit(2, put("../evil", h)), pathx.KindDotSegment)
	wantPathKind(t, "empty path", commit(2, put("", h)), pathx.KindEmpty)
	wantPathKind(t, "duplicate", commit(2, put("n", h), put("n", h)), pathx.KindDuplicate)
	wantPathKind(t, "case conflict", commit(2, put("A.txt", h)), pathx.KindCaseConflict)
	same := contenthash.SumBytes([]byte("hello\n")).String()
	wantCode(t, "no changes", commit(2, put("a.txt", same)), core.CodeInvalidRequest, "reason", core.ReasonNoChanges)
	wantCode(t, "too many changes", commit(2, put("1", h), put("2", h), put("3", h), put("4", h)), core.CodeInvalidRequest,
		"reason", core.ReasonTooManyChanges)
	wantCode(t, "no changes given", commit(2), core.CodeInvalidRequest)
	f, mode := false, 0o600
	both := put("x", h)
	both.Delete = &f
	wantCode(t, "hash and delete", commit(2, both), core.CodeInvalidRequest)
	wantCode(t, "delete false", commit(2, core.Change{Path: "x", Delete: &f}), core.CodeInvalidRequest)
	wantCode(t, "neither", commit(2, core.Change{Path: "x"}), core.CodeInvalidRequest)
	bad := put("x", h)
	bad.Mode = &mode
	wantCode(t, "bad mode", commit(2, bad), core.CodeInvalidRequest)
	wantCode(t, "bad hash", commit(2, put("x", "sha256:ABC")), core.CodeInvalidRequest)
	wantCode(t, "bad branch ID", func() error {
		_, err := e.svc.Commit(ctx, alice, ws, core.CommitRequest{BranchID: "nope", ExpectedGeneration: 2, Changes: []core.Change{put("x", h)}}, "")
		return err
	}(), core.CodeInvalidRequest)
	wantCode(t, "generation 0", commit(0, put("x", h)), core.CodeInvalidRequest)
	long := strings.Repeat("\u00e9", 1025)
	_, err = e.svc.Commit(ctx, alice, ws, core.CommitRequest{BranchID: br, ExpectedGeneration: 2, Message: long, Changes: []core.Change{put("x", h)}}, "")
	wantCode(t, "message too long", err, core.CodeInvalidRequest)
	// Nothing of the failed commits is visible.
	if b, _ := e.store.GetBranch(ctx, nil, br); b.Generation != 2 {
		t.Fatalf("a failed commit moved the branch: %+v", b)
	}
	// A mode-only change is a change.
	exec := 493
	m := put("a.txt", same)
	m.Mode = &exec
	if r := e.commit(alice, ws, br, 2, m); r.Generation != 3 {
		t.Fatal(r)
	}
}

func TestOwnership(t *testing.T) {
	e := newEnv(t, core.Config{})
	sd := e.seed(alice)
	ws, snap := sd.ws.Workspace.ID, sd.first.SnapshotID
	proj := sd.ws.Workspace.ProjectID
	checks := map[string]func() error{
		"get project": func() error { _, err := e.svc.GetProject(ctx, bob, proj); return err },
		"create workspace": func() error {
			_, err := e.svc.CreateWorkspace(ctx, bob, core.CreateWorkspaceRequest{ProjectID: proj}, "")
			return err
		},
		"get workspace": func() error { _, err := e.svc.GetWorkspace(ctx, bob, ws); return err },
		"branches":      func() error { _, _, err := e.svc.ListBranches(ctx, bob, ws, core.Page{}); return err },
		"timeline":      func() error { _, _, err := e.svc.Timeline(ctx, bob, ws, "", core.Page{}); return err },
		"upload": func() error {
			_, _, err := e.svc.PutObject(ctx, bob, core.PutObjectRequest{WorkspaceID: ws, Body: strings.NewReader("x"), ContentLength: -1})
			return err
		},
		"commit": func() error {
			_, err := e.svc.Commit(ctx, bob, ws, core.CommitRequest{BranchID: sd.branch, ExpectedGeneration: 2,
				Changes: []core.Change{del("a.txt")}}, "")
			return err
		},
		"snapshot": func() error { _, err := e.svc.GetSnapshot(ctx, bob, snap); return err },
		"tree":     func() error { _, err := e.svc.ListTree(ctx, bob, snap, "", core.Page{}); return err },
		"file":     func() error { _, err := e.svc.GetFile(ctx, bob, snap, "a.txt"); return err },
		"diff":     func() error { _, err := e.svc.Diff(ctx, bob, snap, "", core.Page{}); return err },
		"restore": func() error {
			_, err := e.svc.Restore(ctx, bob, snap, core.RestoreRequest{BranchID: sd.branch, ExpectedGeneration: 2}, "")
			return err
		},
	}
	for name, fn := range checks {
		wantCode(t, name, fn(), core.CodeNotFound)
	}
	// The same calls by the owner work, and an unknown snapshot is also not found.
	if _, err := e.svc.GetProject(ctx, alice, proj); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.GetSnapshot(ctx, alice, "01JD3X5Q8Z9W2V4T6R8P0N2MZZ")
	wantCode(t, "unknown snapshot", err, core.CodeNotFound)
	// Bob's own workspace: a diff with a base of Alice's workspace is not found.
	bsd := e.seed(bob)
	_, err = e.svc.Diff(ctx, bob, bsd.first.SnapshotID, snap, core.Page{})
	wantCode(t, "diff across workspaces", err, core.CodeNotFound)
	_, err = e.svc.Restore(ctx, bob, bsd.first.SnapshotID, core.RestoreRequest{BranchID: sd.branch, ExpectedGeneration: 2}, "")
	wantCode(t, "restore onto a branch of another workspace", err, core.CodeNotFound)
}

func TestIdempotency(t *testing.T) {
	e := newEnv(t, core.Config{})
	sd := e.seed(alice)
	ws, br := sd.ws.Workspace.ID, sd.branch
	h := e.upload(alice, ws, "idempotent")
	req := core.CommitRequest{BranchID: br, ExpectedGeneration: 2, Changes: []core.Change{put("c.txt", h)}}
	first, err := e.svc.Commit(ctx, alice, ws, req, "key-1")
	e.must(err)
	retry, err := e.svc.Commit(ctx, alice, ws, req, "key-1")
	e.must(err)
	if retry.SnapshotID != first.SnapshotID || retry.Generation != first.Generation || retry.CreatedAtUs != first.CreatedAtUs {
		t.Fatalf("retry %+v, first %+v", retry, first)
	}
	snaps, _, _ := e.svc.Timeline(ctx, alice, ws, "", core.Page{})
	if len(snaps) != 2 {
		t.Fatalf("the retry created a snapshot: %d snapshots", len(snaps))
	}
	other := req
	other.Changes = []core.Change{put("d.txt", h)}
	_, err = e.svc.Commit(ctx, alice, ws, other, "key-1")
	wantCode(t, "same key, other body", err, core.CodeIdempotencyKeyReuse)
	// Another actor with the same key is another record.
	if _, err := e.svc.CreateProject(ctx, bob, core.CreateProjectRequest{Name: "b"}, "key-1"); err != nil {
		t.Fatal(err)
	}
	// A failed request does not keep its key: the corrected request runs.
	stale := other
	stale.ExpectedGeneration = 2
	_, err = e.svc.Commit(ctx, alice, ws, stale, "key-2")
	wantCode(t, "stale generation with a key", err, core.CodeConflict)
	stale.ExpectedGeneration = 3
	if _, err := e.svc.Commit(ctx, alice, ws, stale, "key-2"); err != nil {
		t.Fatalf("retry after a failure: %v", err)
	}
	// An in-progress record (a request still running) blocks the key.
	e.must(e.store.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		return e.store.InsertIdempotency(ctx, tx, ports.IdempotencyRecord{ActorRef: "alice", Route: core.RouteCreateProject,
			Key: "busy", RequestHash: mustHash(t, core.CreateProjectRequest{Name: "p"}), Status: ports.IdempotencyInProgress,
			CreatedAtUs: e.clock.Now().UnixMicro(), ExpiresAtUs: e.clock.Now().Add(time.Hour).UnixMicro()})
	}))
	_, err = e.svc.CreateProject(ctx, alice, core.CreateProjectRequest{Name: "p"}, "busy")
	wantCode(t, "in progress", err, core.CodeConflict, "reason", core.ReasonInProgress)
	// After 24 hours a key is free again.
	p1, err := e.svc.CreateProject(ctx, alice, core.CreateProjectRequest{Name: "q"}, "day")
	e.must(err)
	p2, err := e.svc.CreateProject(ctx, alice, core.CreateProjectRequest{Name: "q"}, "day")
	e.must(err)
	if p1.ID != p2.ID {
		t.Fatal("replay of create project returned another project")
	}
	e.clock.Advance(24*time.Hour + time.Second)
	p3, err := e.svc.CreateProject(ctx, alice, core.CreateProjectRequest{Name: "q"}, "day")
	e.must(err)
	if p3.ID == p1.ID {
		t.Fatal("an expired key was replayed")
	}
	// Workspace creation and restore replay too.
	w1, err := e.svc.CreateWorkspace(ctx, alice, core.CreateWorkspaceRequest{ProjectID: p3.ID}, "ws")
	e.must(err)
	w2, err := e.svc.CreateWorkspace(ctx, alice, core.CreateWorkspaceRequest{ProjectID: p3.ID}, "ws")
	e.must(err)
	if w1.Workspace.ID != w2.Workspace.ID || w1.MainBranchID != w2.MainBranchID {
		t.Fatal("replay of create workspace differs")
	}
	rr := core.RestoreRequest{BranchID: br, ExpectedGeneration: 4}
	r1, err := e.svc.Restore(ctx, alice, sd.first.SnapshotID, rr, "r")
	e.must(err)
	r2, err := e.svc.Restore(ctx, alice, sd.first.SnapshotID, rr, "r")
	e.must(err)
	if r1.SnapshotID != r2.SnapshotID {
		t.Fatal("replay of restore differs")
	}
	_, err = e.svc.CreateProject(ctx, alice, core.CreateProjectRequest{Name: "q"}, "bad key")
	wantCode(t, "key with a space", err, core.CodeInvalidRequest)
}

func mustHash(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return contenthash.SumBytes(b).Hex()
}

func TestUpload(t *testing.T) {
	e := newEnv(t, core.Config{MaxUploadBytes: 10})
	ws := e.workspace(alice).Workspace.ID
	up := func(content string, n int64, expected string) (ports.Blob, bool, error) {
		return e.svc.PutObject(ctx, alice, core.PutObjectRequest{WorkspaceID: ws, Body: strings.NewReader(content),
			ContentLength: n, Expected: expected})
	}
	b, created, err := up("hello", 5, contenthash.SumBytes([]byte("hello")).String())
	if err != nil || !created || b.Status != ports.BlobStaged || b.Size != 5 {
		t.Fatalf("first upload: %+v %v %v", b, created, err)
	}
	b, created, err = up("hello", -1, "")
	if err != nil || created || b.Status != ports.BlobStaged {
		t.Fatalf("second upload: %+v %v %v", b, created, err)
	}
	if b, _, err := up("", 0, ""); err != nil || b.Hash != contenthash.SumBytes(nil) {
		t.Fatalf("empty file: %+v %v", b, err)
	}
	_, _, err = up("hello", -1, contenthash.SumBytes([]byte("other")).String())
	wantCode(t, "checksum mismatch", err, core.CodeInvalidRequest, "reason", core.ReasonChecksumMismatch)
	_, _, err = up("01234567890", -1, "")
	wantCode(t, "too large while streaming", err, core.CodeFileTooLarge)
	_, _, err = up("x", 11, "")
	wantCode(t, "too large by Content-Length", err, core.CodeFileTooLarge)
	_, _, err = up("x", -1, "sha256:XYZ")
	wantCode(t, "malformed checksum", err, core.CodeInvalidRequest)
	if n := len(e.objs.Keys()); n != 2 {
		t.Fatalf("%d objects stored, want 2 (hello and the empty file)", n)
	}
}

func TestRestorePath(t *testing.T) {
	e := newEnv(t, core.Config{})
	ws := e.workspace(alice)
	id, br := ws.Workspace.ID, ws.MainBranchID
	up := func(s string) string { return e.upload(alice, id, s) }
	first := e.commit(alice, id, br, 1, put("docs/a.txt", up("a1")), put("docs/b.txt", up("b1")), put("keep.txt", up("k")))
	second := e.commit(alice, id, br, 2, put("docs/a.txt", up("a2")), del("docs/b.txt"), put("new/x.txt", up("x")))
	restore := func(path string, gen int64) (core.CommitResult, error) {
		return e.svc.Restore(ctx, alice, first.SnapshotID, core.RestoreRequest{BranchID: br, ExpectedGeneration: gen, Path: &path}, "")
	}
	r, err := restore("docs/a.txt", 3)
	e.must(err)
	if got := e.read(alice, r.SnapshotID, "docs/a.txt"); got != "a1" {
		t.Fatalf("restored file: %q", got)
	}
	if got := e.read(alice, r.SnapshotID, "new/x.txt"); got != "x" {
		t.Fatalf("kept file: %q", got)
	}
	// A directory: docs/b.txt comes back.
	r, err = restore("docs", 4)
	e.must(err)
	if got := e.read(alice, r.SnapshotID, "docs/b.txt"); got != "b1" {
		t.Fatalf("restored directory: %q", got)
	}
	// A path the target does not have is removed from the head.
	r, err = restore("new", 5)
	e.must(err)
	if _, err := e.svc.GetFile(ctx, alice, r.SnapshotID, "new/x.txt"); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("removed path: %v", err)
	}
	snap, _ := e.svc.GetSnapshot(ctx, alice, r.SnapshotID)
	if snap.EntryCount != 3 || snap.TotalBytes != 5 {
		t.Fatalf("counts after restores: %+v", snap)
	}
	// The head now equals the first snapshot.
	if r.RootTreeHash != first.RootTreeHash {
		t.Fatalf("root after restores %s, want %s", r.RootTreeHash, first.RootTreeHash)
	}
	_, err = restore("nowhere", 6)
	wantCode(t, "path in neither tree", err, core.CodeInvalidRequest, "reason", core.ReasonNoChanges)
	_, err = restore("docs", 6)
	wantCode(t, "restore of an equal path", err, core.CodeInvalidRequest, "reason", core.ReasonNoChanges)
	_, err = restore("../x", 6)
	wantPathKind(t, "invalid restore path", err, pathx.KindDotSegment)
	_, err = restore("docs", 2)
	wantCode(t, "stale generation", err, core.CodeConflict, "current_generation", 6)
	_ = second
}

// TestRestorePathCaseConflict: a restored name the head does not have, and
// a directory above it the head does not have, are new names of their
// directory and must not conflict in case with the names there.
func TestRestorePathCaseConflict(t *testing.T) {
	e := newEnv(t, core.Config{})
	ws := e.workspace(alice)
	id, br := ws.Workspace.ID, ws.MainBranchID
	up := func(s string) string { return e.upload(alice, id, s) }
	first := e.commit(alice, id, br, 1, put("README.md", up("upper")), put("Docs/a.txt", up("a")))
	e.commit(alice, id, br, 2, del("README.md"), del("Docs/a.txt"), put("keep.txt", up("k")))
	e.commit(alice, id, br, 3, put("readme.md", up("lower")), put("docs/b.txt", up("b")))
	restore := func(path string, gen int64) (core.CommitResult, error) {
		return e.svc.Restore(ctx, alice, first.SnapshotID, core.RestoreRequest{BranchID: br, ExpectedGeneration: gen, Path: &path}, "")
	}
	for _, p := range []string{"README.md", "Docs", "Docs/a.txt"} {
		_, err := restore(p, 4)
		wantPathKind(t, "restore "+p, err, pathx.KindCaseConflict)
	}
	// Once readme.md and docs/ are deleted, both can be restored.
	e.commit(alice, id, br, 4, del("readme.md"), del("docs/b.txt"))
	r, err := restore("README.md", 5)
	e.must(err)
	r, err = restore("Docs/a.txt", 6)
	e.must(err)
	if got := e.read(alice, r.SnapshotID, "README.md") + e.read(alice, r.SnapshotID, "Docs/a.txt"); got != "uppera" {
		t.Fatalf("restored files: %q", got)
	}
}

// TestLabels: project names and workspace display names are free text of
// at most 255 bytes without control characters, noncharacters, bidi
// controls or invisible characters; a project name is not only white space.
func TestLabels(t *testing.T) {
	e := newEnv(t, core.Config{})
	for _, name := range []string{"demo", "Plan 2027", "\u6587\u4ef6", strings.Repeat("a", 255), strings.Repeat("\u6587", 85),
		" padded ", "\U0001F469\u200d\U0001F4BB"} {
		if p, err := e.svc.CreateProject(ctx, alice, core.CreateProjectRequest{Name: name}, ""); err != nil || p.Name != name {
			t.Errorf("name %q: %q %v", name, p.Name, err)
		}
	}
	for what, name := range map[string]string{
		"empty": "", "white space": " \t\u3000", "256 bytes": strings.Repeat("a", 256),
		"86 CJK characters (258 bytes)": strings.Repeat("\u6587", 86), "a line feed": "a\nb", "DEL": "a\x7fb",
		"a C1 control": "a\u0085b", "a bidi override": "abc\u202edef", "a zero width space": "a\u200bb",
		"a noncharacter": "a\ufdd0", "invalid UTF-8": "a\xffb", "a Hangul filler": "\u3164",
	} {
		_, err := e.svc.CreateProject(ctx, alice, core.CreateProjectRequest{Name: name}, "")
		wantCode(t, "name with "+what, err, core.CodeInvalidRequest)
	}
	proj, err := e.svc.CreateProject(ctx, alice, core.CreateProjectRequest{Name: "p"}, "")
	e.must(err)
	for _, dn := range []string{"", "Main workspace"} {
		_, err := e.svc.CreateWorkspace(ctx, alice, core.CreateWorkspaceRequest{ProjectID: proj.ID, DisplayName: dn}, "")
		e.must(err)
	}
	for what, dn := range map[string]string{"256 bytes": strings.Repeat("b", 256), "a bidi override": "\u202e", "a carriage return": "a\rb"} {
		_, err := e.svc.CreateWorkspace(ctx, alice, core.CreateWorkspaceRequest{ProjectID: proj.ID, DisplayName: dn}, "")
		wantCode(t, "display name with "+what, err, core.CodeInvalidRequest)
	}
}

func TestListsAndPagination(t *testing.T) {
	e := newEnv(t, core.Config{})
	ws := e.workspace(alice)
	id, br := ws.Workspace.ID, ws.MainBranchID
	h := e.upload(alice, id, "content")
	var commits []core.CommitResult
	for i := 0; i < 5; i++ {
		commits = append(commits, e.commit(alice, id, br, int64(i+1), put(fmt.Sprintf("dir/f%d", i), h)))
	}
	var ids []string
	cursor := ""
	for {
		page, next, err := e.svc.Timeline(ctx, alice, id, br, core.Page{Limit: 2, Cursor: cursor})
		e.must(err)
		for _, s := range page {
			ids = append(ids, s.ID)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(ids) != 5 || ids[0] != commits[4].SnapshotID || ids[4] != commits[0].SnapshotID {
		t.Fatalf("timeline: %v", ids)
	}
	var names []string
	cursor = ""
	for {
		l, err := e.svc.ListTree(ctx, alice, commits[4].SnapshotID, "dir", core.Page{Limit: 2, Cursor: cursor})
		e.must(err)
		for _, x := range l.Items {
			names = append(names, x.Entry.Name)
		}
		if l.NextCursor == "" {
			break
		}
		cursor = l.NextCursor
	}
	if strings.Join(names, ",") != "f0,f1,f2,f3,f4" {
		t.Fatalf("listing: %v", names)
	}
	d, err := e.svc.Diff(ctx, alice, commits[4].SnapshotID, commits[0].SnapshotID, core.Page{Limit: 3})
	e.must(err)
	d2, err := e.svc.Diff(ctx, alice, commits[4].SnapshotID, commits[0].SnapshotID, core.Page{Limit: 3, Cursor: d.NextCursor})
	e.must(err)
	if len(d.Items) != 3 || len(d2.Items) != 1 || d2.NextCursor != "" || d2.Items[0].Path != "dir/f4" {
		t.Fatalf("diff pages: %+v / %+v", d.Items, d2.Items)
	}
	bs, next, err := e.svc.ListBranches(ctx, alice, id, core.Page{Limit: 1})
	if err != nil || len(bs) != 1 || bs[0].RefName != "main" || next != "" || bs[0].Generation != 6 {
		t.Fatalf("branches: %+v %q %v", bs, next, err)
	}
	for name, fn := range map[string]func() error{
		"limit 0 is the default": func() error { _, _, err := e.svc.Timeline(ctx, alice, id, "", core.Page{}); return err },
	} {
		if err := fn(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, fn := range map[string]func() error{
		"limit 501": func() error { _, _, err := e.svc.Timeline(ctx, alice, id, "", core.Page{Limit: 501}); return err },
		"limit -1":  func() error { _, _, err := e.svc.ListBranches(ctx, alice, id, core.Page{Limit: -1}); return err },
		"bad cursor": func() error {
			_, err := e.svc.ListTree(ctx, alice, commits[0].SnapshotID, "", core.Page{Cursor: "!!"})
			return err
		},
		"cursor of another list": func() error {
			_, _, err := e.svc.Timeline(ctx, alice, id, "", core.Page{Cursor: d.NextCursor})
			return err
		},
		"tree of a file": func() error {
			_, err := e.svc.ListTree(ctx, alice, commits[0].SnapshotID, "dir/f0", core.Page{})
			return err
		},
		"file of a directory": func() error { _, err := e.svc.GetFile(ctx, alice, commits[0].SnapshotID, "dir"); return err },
	} {
		wantCode(t, name, fn(), core.CodeInvalidRequest)
	}
	_, err = e.svc.GetFile(ctx, alice, commits[0].SnapshotID, "dir/missing")
	wantCode(t, "missing file", err, core.CodeNotFound)
	_, err = e.svc.ListTree(ctx, alice, commits[0].SnapshotID, "missing", core.Page{})
	wantCode(t, "missing directory", err, core.CodeNotFound)
	_, err = e.svc.GetFile(ctx, alice, commits[0].SnapshotID, "dir//f0")
	wantPathKind(t, "lookup with an empty segment", err, pathx.KindEmptySegment)
	_, _, err = e.svc.Timeline(ctx, alice, id, e.workspace(alice).MainBranchID, core.Page{})
	wantCode(t, "timeline of another workspace's branch", err, core.CodeNotFound)
}

func TestConcurrentCommitsOneWins(t *testing.T) {
	e := newEnv(t, core.Config{})
	sd := e.seed(alice)
	ws := sd.ws.Workspace.ID
	var hashes []string
	for i := 0; i < 6; i++ {
		hashes = append(hashes, e.upload(alice, ws, fmt.Sprint("writer ", i)))
	}
	var wg sync.WaitGroup
	errs := make([]error, len(hashes))
	for i, h := range hashes {
		wg.Add(1)
		go func(i int, h string) {
			defer wg.Done()
			_, errs[i] = e.svc.Commit(ctx, alice, ws, core.CommitRequest{BranchID: sd.branch, ExpectedGeneration: 2,
				Changes: []core.Change{put("a.txt", h)}}, "")
		}(i, h)
	}
	wg.Wait()
	ok, conflicts := 0, 0
	for _, err := range errs {
		switch core.CodeOf(err) {
		case "":
			ok++
		case core.CodeConflict:
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if ok != 1 || conflicts != len(hashes)-1 {
		t.Fatalf("%d commits succeeded, %d conflicted", ok, conflicts)
	}
}

func TestDownloadOfLostContent(t *testing.T) {
	e := newEnv(t, core.Config{})
	sd := e.seed(alice)
	f, err := e.svc.GetFile(ctx, alice, sd.first.SnapshotID, "a.txt")
	e.must(err)
	e.must(e.objs.Delete(ctx, storagekey.Join(sd.ws.Workspace.StoragePrefix, storagekey.ObjectKey(f.Entry.Hash))))
	_, err = f.Open(ctx, 0, -1)
	wantCode(t, "lost content", err, core.CodeBlobMissing, "reason", core.ReasonMissingInStorage)
}

// TestUploadLimitAtInt64Max: the largest limit there is stores the content
// (a limit+1 overflow once made the stores read nothing).
func TestUploadLimitAtInt64Max(t *testing.T) {
	e := newEnv(t, core.Config{MaxUploadBytes: math.MaxInt64})
	sd := e.seed(alice)
	if got := e.read(alice, sd.first.SnapshotID, "docs/b.txt"); got != "second file\n" {
		t.Fatalf("content stored with the limit MaxInt64: %q", got)
	}
}

// TestSnapshotTimesNeverGoBack: a snapshot is never older than its parent,
// even when the clock goes back.
func TestSnapshotTimesNeverGoBack(t *testing.T) {
	e := newEnv(t, core.Config{})
	sd := e.seed(alice)
	id := sd.ws.Workspace.ID
	e.clock.Advance(-time.Hour)
	second := e.commit(alice, id, sd.branch, 2, put("c.txt", e.upload(alice, id, "c")))
	third := e.commit(alice, id, sd.branch, 3, put("d.txt", e.upload(alice, id, "d")))
	restored, err := e.svc.Restore(ctx, alice, sd.first.SnapshotID, core.RestoreRequest{BranchID: sd.branch, ExpectedGeneration: 4}, "")
	e.must(err)
	for i, pair := range [][2]int64{{sd.first.CreatedAtUs, second.CreatedAtUs}, {second.CreatedAtUs, third.CreatedAtUs},
		{third.CreatedAtUs, restored.CreatedAtUs}} {
		if pair[1] != pair[0]+1 {
			t.Errorf("snapshot %d: created at %d, parent at %d; want the parent's time plus 1", i+2, pair[1], pair[0])
		}
	}
	snap, err := e.svc.GetSnapshot(ctx, alice, restored.SnapshotID)
	e.must(err)
	if snap.CreatedAtUs != restored.CreatedAtUs {
		t.Errorf("stored time %d, returned %d", snap.CreatedAtUs, restored.CreatedAtUs)
	}
	// A clock that is ahead again is used as it is.
	e.clock.Advance(2 * time.Hour)
	fourth := e.commit(alice, id, sd.branch, 5, put("e.txt", e.upload(alice, id, "e")))
	if fourth.CreatedAtUs != e.clock.Now().UnixMicro() {
		t.Errorf("created at %d, clock %d", fourth.CreatedAtUs, e.clock.Now().UnixMicro())
	}
}
