package core_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/authz"
	"github.com/LUOSENGWA/MFGraph/internal/core"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/ports/memory"
	"github.com/LUOSENGWA/MFGraph/internal/treestore"
)

const (
	moduleTenant = "01JD3X5Q8Z9W2V4T6R8P0N2T00"
	projP        = "01JD3X5Q8Z9W2V4T6R8P0N2P01"
	projQ        = "01JD3X5Q8Z9W2V4T6R8P0N2P02"
)

// Principals of module mode: the host admitted them to one project each.
var (
	managerP = ports.Principal{Ref: "manager", ActorType: "human", ProjectScope: projP}
	managerQ = ports.Principal{Ref: "manager", ActorType: "human", ProjectScope: projQ}
	workerP  = ports.Principal{Ref: "worker-7", ActorType: "worker", ProjectScope: projP, OnBehalfOfRef: "manager",
		OnBehalfOfType: "human"}
)

// noProjects fails the test when the core touches the projects table,
// which does not exist in module mode.
type noProjects struct {
	ports.Store
	t *testing.T
}

func (s noProjects) InsertProject(context.Context, ports.Tx, ports.Project) error {
	s.t.Error("module mode inserted a project")
	return ports.ErrDBUnavailable
}

func (s noProjects) GetProject(context.Context, ports.Tx, string) (ports.Project, error) {
	s.t.Error("module mode read the projects table")
	return ports.Project{}, ports.ErrDBUnavailable
}

// newModuleEnv is newEnv in module mode: the host owns the projects and the
// authorizer checks the project scope.
func newModuleEnv(t *testing.T, cfg core.Config) *env {
	t.Helper()
	store := memory.NewStore()
	objs := memory.NewBlobStore()
	trees, err := treestore.New(objs, treestore.CachedPrefixes(func(ctx context.Context, ws string) (string, error) {
		w, err := store.GetWorkspace(ctx, nil, ws)
		return w.StoragePrefix, err
	}, 100))
	if err != nil {
		t.Fatal(err)
	}
	clock := memory.NewClock(time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC))
	cfg.TenantID, cfg.HostOwnsProjects = moduleTenant, true
	logs := &syncBuffer{}
	svc, err := core.New(cfg, core.Deps{Store: noProjects{store, t}, Objects: objs, Trees: trees, Authz: authz.ProjectScope{},
		Clock: clock, IDs: memory.NewIDGen(clock), Logger: slog.New(slog.NewTextHandler(logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, svc: svc, store: store, objs: objs, clock: clock, logs: logs}
}

// moduleSeed creates a workspace of project P with one commit.
func (e *env) moduleSeed(p ports.Principal) seeded {
	e.t.Helper()
	ws, err := e.svc.CreateWorkspace(ctx, p, core.CreateWorkspaceRequest{ProjectID: p.ProjectScope, DisplayName: "w"}, "")
	e.must(err)
	id := ws.Workspace.ID
	first := e.commit(p, id, ws.MainBranchID, 1, put("a.txt", e.upload(p, id, "hello\n")))
	return seeded{ws: ws, branch: ws.MainBranchID, first: first}
}

// TestModuleContainment is TestOwnership in module mode: every operation
// on a resource of another project gives not_found with exactly the error
// of an ID that does not exist, and the project operations do not exist.
func TestModuleContainment(t *testing.T) {
	e := newModuleEnv(t, core.Config{})
	sd := e.moduleSeed(managerP)
	ws, snap, branch := sd.ws.Workspace.ID, sd.first.SnapshotID, sd.branch
	if sd.ws.Workspace.ProjectID != projP || sd.ws.Workspace.TenantID != moduleTenant {
		t.Fatalf("workspace: %+v", sd.ws.Workspace)
	}
	const unknown = "01JD3X5Q8Z9W2V4T6R8P0N2MZZ"
	type call func(p ports.Principal, id string) error
	onWorkspace := map[string]call{
		"get workspace": func(p ports.Principal, id string) error { _, err := e.svc.GetWorkspace(ctx, p, id); return err },
		"branches": func(p ports.Principal, id string) error {
			_, _, err := e.svc.ListBranches(ctx, p, id, core.Page{})
			return err
		},
		"timeline": func(p ports.Principal, id string) error {
			_, _, err := e.svc.Timeline(ctx, p, id, "", core.Page{})
			return err
		},
		"upload": func(p ports.Principal, id string) error {
			_, _, err := e.svc.PutObject(ctx, p, core.PutObjectRequest{WorkspaceID: id, Body: strings.NewReader("x"), ContentLength: -1})
			return err
		},
		"commit": func(p ports.Principal, id string) error {
			_, err := e.svc.Commit(ctx, p, id, core.CommitRequest{BranchID: branch, ExpectedGeneration: 2,
				Changes: []core.Change{del("a.txt")}}, "")
			return err
		},
	}
	onSnapshot := map[string]call{
		"snapshot": func(p ports.Principal, id string) error { _, err := e.svc.GetSnapshot(ctx, p, id); return err },
		"tree": func(p ports.Principal, id string) error {
			_, err := e.svc.ListTree(ctx, p, id, "", core.Page{})
			return err
		},
		"file": func(p ports.Principal, id string) error { _, err := e.svc.GetFile(ctx, p, id, "a.txt"); return err },
		"diff": func(p ports.Principal, id string) error {
			_, err := e.svc.Diff(ctx, p, id, "", core.Page{})
			return err
		},
		"restore": func(p ports.Principal, id string) error {
			_, err := e.svc.Restore(ctx, p, id, core.RestoreRequest{BranchID: branch, ExpectedGeneration: 2}, "")
			return err
		},
	}
	same := func(name string, cross, missing error) {
		t.Helper()
		var a, b *core.Error
		if !errorAs(cross, &a) || !errorAs(missing, &b) || a.Code != core.CodeNotFound || a.Code != b.Code ||
			a.Message != b.Message || len(a.Details) != 0 || len(b.Details) != 0 {
			t.Errorf("%s: another project gives %v, an unknown ID %v; want the same not_found", name, cross, missing)
		}
	}
	for name, fn := range onWorkspace {
		same(name, fn(managerQ, ws), fn(managerQ, unknown))
		if err := fn(managerP, ws); err != nil && name != "commit" {
			t.Errorf("%s in its own project: %v", name, err)
		}
	}
	for name, fn := range onSnapshot {
		same(name, fn(managerQ, snap), fn(managerQ, unknown))
	}
	// A workspace is created only in the project of the request, which
	// is not looked up anywhere.
	_, err := e.svc.CreateWorkspace(ctx, managerQ, core.CreateWorkspaceRequest{ProjectID: projP}, "")
	wantCode(t, "workspace in another project", err, core.CodeNotFound)
	other, err := e.svc.CreateWorkspace(ctx, managerQ, core.CreateWorkspaceRequest{ProjectID: projQ}, "")
	if err != nil || other.Workspace.ProjectID != projQ {
		t.Fatalf("workspace in the project of the request: %+v %v", other, err)
	}
	// A diff with a base of another project, and a restore onto a branch
	// of another project's workspace.
	_, err = e.svc.Diff(ctx, managerQ, other.Workspace.ID, snap, core.Page{})
	wantCode(t, "diff across projects", err, core.CodeNotFound)
	// The project operations do not exist.
	_, err = e.svc.CreateProject(ctx, managerP, core.CreateProjectRequest{Name: "p"}, "")
	wantCode(t, "create project", err, core.CodeNotFound)
	_, err = e.svc.GetProject(ctx, managerP, projP)
	wantCode(t, "get project", err, core.CodeNotFound)
	// Another actor of the same project sees everything.
	if _, err := e.svc.GetSnapshot(ctx, workerP, snap); err != nil {
		t.Fatalf("another actor of the project: %v", err)
	}
}

// TestActorTypes: module mode records the actor types and the actor acted
// for with the snapshot and the workspace; an idempotency record is
// replayed only to an actor of the same type (no type and no type match).
func TestActorTypes(t *testing.T) {
	e := newModuleEnv(t, core.Config{})
	sd := e.moduleSeed(workerP)
	snap, err := e.svc.GetSnapshot(ctx, workerP, sd.first.SnapshotID)
	if err != nil || snap.AuthorRef != "worker-7" || snap.AuthorType != "worker" || snap.OnBehalfOfRef != "manager" ||
		snap.OnBehalfOfType != "human" || snap.AuthorPrincipalUID != "" {
		t.Fatalf("snapshot actors: %+v %v", snap, err)
	}
	if ws, err := e.store.GetWorkspace(ctx, nil, sd.ws.Workspace.ID); err != nil || ws.OwnerRef != "worker-7" || ws.OwnerType != "worker" {
		t.Fatalf("workspace owner: %+v %v", ws, err)
	}

	// The same reference with another type is another actor.
	humanWorker := ports.Principal{Ref: "worker-7", ActorType: "human", ProjectScope: projP}
	untyped := ports.Principal{Ref: "worker-7", ProjectScope: projP}
	req := core.CreateWorkspaceRequest{ProjectID: projP, DisplayName: "idem"}
	first, err := e.svc.CreateWorkspace(ctx, workerP, req, "key-1")
	e.must(err)
	again, err := e.svc.CreateWorkspace(ctx, workerP, req, "key-1")
	if err != nil || again.Workspace.ID != first.Workspace.ID {
		t.Fatalf("replay to the same actor: %+v %v", again, err)
	}
	for _, p := range []ports.Principal{humanWorker, untyped} {
		_, err := e.svc.CreateWorkspace(ctx, p, req, "key-1")
		wantCode(t, "replay to an actor of type "+p.ActorType, err, core.CodeIdempotencyKeyReuse)
	}
	// An untyped record replays to the untyped actor only.
	u1, err := e.svc.CreateWorkspace(ctx, untyped, req, "key-2")
	e.must(err)
	if u2, err := e.svc.CreateWorkspace(ctx, untyped, req, "key-2"); err != nil || u2.Workspace.ID != u1.Workspace.ID {
		t.Fatalf("untyped replay: %+v %v", u2, err)
	}
	_, err = e.svc.CreateWorkspace(ctx, workerP, req, "key-2")
	wantCode(t, "typed actor on an untyped record", err, core.CodeIdempotencyKeyReuse)
	// Once expired, the record of the other type is replaced.
	e.clock.Advance(25 * time.Hour)
	if r, err := e.svc.CreateWorkspace(ctx, humanWorker, req, "key-1"); err != nil || r.Workspace.ID == first.Workspace.ID {
		t.Fatalf("after expiry: %+v %v", r, err)
	}
}

// TestStandaloneHasNoActorTypes: standalone principals have no type, so
// nothing is recorded.
func TestStandaloneHasNoActorTypes(t *testing.T) {
	e := newEnv(t, core.Config{})
	sd := e.seed(alice)
	snap, err := e.svc.GetSnapshot(ctx, alice, sd.first.SnapshotID)
	if err != nil || snap.AuthorType != "" || snap.OnBehalfOfRef != "" || snap.OnBehalfOfType != "" {
		t.Fatalf("standalone snapshot: %+v %v", snap, err)
	}
}
