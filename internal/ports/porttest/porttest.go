// Package porttest is the contract test suite of ports.Store. The in-memory
// store runs it in the unit tests, the MySQL store against MySQL 8.0 and
// seekdb, so that both implementations behave alike.
package porttest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/storagekey"
)

// EventReader lists the committed outbox events; both stores provide it
// for tests and diagnostics.
type EventReader interface {
	Events(ctx context.Context) ([]ports.Event, error)
}

// Store is what the suite needs: a ports.Store whose events can be read
// and delivered.
type Store interface {
	ports.Store
	EventReader
	ports.OutboxQueue
	EventDelivery(ctx context.Context, id string) (ports.EventDelivery, error)
}

// Run runs the contract tests. newStore must return an empty store; it is
// called once per subtest.
func Run(t *testing.T, newStore func(t *testing.T) Store) {
	tests := []struct {
		name string
		fn   func(*testing.T, Store)
	}{
		{"Transactions", testTransactions},
		{"Projects", testProjects},
		{"Workspaces", testWorkspaces},
		{"Branches", testBranches},
		{"BranchCAS", testBranchCAS},
		{"ConcurrentCAS", testConcurrentCAS},
		{"Snapshots", testSnapshots},
		{"Timeline", testTimeline},
		{"Blobs", testBlobs},
		{"PackedLayout", testPackedLayout},
		{"Idempotency", testIdempotency},
		{"Outbox", testOutbox},
		{"OutboxQueue", testOutboxQueue},
		{"Usage", testUsage},
		{"UploadSessions", testUploadSessions},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { tc.fn(t, newStore(t)) })
	}
}

var ctx = context.Background()

// IDs used by the suite (valid ULIDs).
const (
	tenant = "00000000000000000000000000"
	projA  = "01JD3X5Q8Z9W2V4T6R8P0N2PA0"
	projB  = "01JD3X5Q8Z9W2V4T6R8P0N2PB0"
	wsA    = "01JD3X5Q8Z9W2V4T6R8P0N2WA0"
	wsB    = "01JD3X5Q8Z9W2V4T6R8P0N2WB0"
	brA    = "01JD3X5Q8Z9W2V4T6R8P0N2BA0"
	brB    = "01JD3X5Q8Z9W2V4T6R8P0N2BB0"
)

func tx(t *testing.T, s Store, fn func(tx ports.Tx) error) {
	t.Helper()
	if err := s.WithTx(ctx, func(_ context.Context, tx ports.Tx) error { return fn(tx) }); err != nil {
		t.Fatal(err)
	}
}

func project(id, owner string) ports.Project {
	return ports.Project{ID: id, TenantID: tenant, Name: "project " + id[len(id)-2:], Status: ports.StatusActive,
		OwnerRef: owner, CreatedAtUs: 1_000_001, UpdatedAtUs: 1_000_002}
}

func workspace(id, projectID, backend string) ports.Workspace {
	prefix, err := storagekey.DefaultPrefix(tenant, projectID, id)
	if err != nil {
		panic(err)
	}
	return ports.Workspace{ID: id, TenantID: tenant, ProjectID: projectID, Type: ports.WorkspaceTypeProject,
		OwnerRef: "alice", StorageBackendID: backend, StoragePrefix: prefix, StoragePrefixKey: storagekey.PrefixKey(prefix),
		DisplayName: "ws", Status: ports.StatusActive, CreatedAtUs: 2_000_001, UpdatedAtUs: 2_000_002}
}

func mainBranch(id, ws, project string) ports.Branch {
	return ports.Branch{ID: id, WorkspaceID: ws, ProjectID: project, Name: ports.MainRefName, RefName: ports.MainRefName,
		Type: ports.BranchTypeMain, Generation: 1, Status: ports.StatusActive, CreatedByRef: "alice",
		CreatedAtUs: 3_000_001, UpdatedAtUs: 3_000_001}
}

func hashOf(s string) contenthash.Hash { return contenthash.SumBytes([]byte(s)) }

func wantErr(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: err = %v, want %v", what, err, want)
	}
}

func testTransactions(t *testing.T, s Store) {
	// Rollback discards every write of the transaction.
	boom := errors.New("boom")
	err := s.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := s.InsertProject(ctx, tx, project(projA, "alice")); err != nil {
			return err
		}
		if _, err := s.GetProject(ctx, tx, projA); err != nil {
			return fmt.Errorf("a transaction must see its own write: %w", err)
		}
		if _, err := s.GetProject(ctx, nil, projA); !errors.Is(err, ports.ErrNotFound) {
			return fmt.Errorf("uncommitted write visible outside the transaction: %v", err)
		}
		return boom
	})
	wantErr(t, "rolled back transaction", err, boom)
	if _, err := s.GetProject(ctx, nil, projA); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("rolled back insert is visible: %v", err)
	}
	// A finished transaction cannot be used again.
	var leaked ports.Tx
	tx(t, s, func(tx ports.Tx) error { leaked = tx; return nil })
	if err := s.InsertProject(ctx, leaked, project(projB, "alice")); err == nil {
		t.Fatal("insert through a finished transaction succeeded")
	}
	// A write needs a transaction.
	if err := s.InsertProject(ctx, nil, project(projB, "alice")); err == nil {
		t.Fatal("insert without a transaction succeeded")
	}
	// A failed statement inside a transaction does not abort it.
	tx(t, s, func(tx ports.Tx) error {
		if err := s.InsertProject(ctx, tx, project(projA, "alice")); err != nil {
			return err
		}
		if err := s.InsertProject(ctx, tx, project(projA, "alice")); !errors.Is(err, ports.ErrAlreadyExists) {
			return fmt.Errorf("duplicate insert: %v", err)
		}
		return s.InsertProject(ctx, tx, project(projB, "bob"))
	})
	for _, id := range []string{projA, projB} {
		if _, err := s.GetProject(ctx, nil, id); err != nil {
			t.Fatalf("project %s after commit: %v", id, err)
		}
	}
}

func testProjects(t *testing.T, s Store) {
	want := project(projA, "@alice:example.com")
	want.Name = "Zeichnungen \u56fe\u7eb8 \u00e9 "
	want.OwnerPrincipalUID = "01JD3X5Q8Z9W2V4T6R8P0N2M4K"
	tx(t, s, func(tx ports.Tx) error { return s.InsertProject(ctx, tx, want) })
	got, err := s.GetProject(ctx, nil, projA)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("GetProject = %+v, %v\nwant %+v", got, err, want)
	}
	_, err = s.GetProject(ctx, nil, projB)
	wantErr(t, "missing project", err, ports.ErrNotFound)
	// A project without owner reads back with an empty owner.
	noOwner := project(projB, "")
	tx(t, s, func(tx ports.Tx) error { return s.InsertProject(ctx, tx, noOwner) })
	if got, err := s.GetProject(ctx, nil, projB); err != nil || got.OwnerRef != "" || got.OwnerPrincipalUID != "" {
		t.Fatalf("project without owner: %+v, %v", got, err)
	}
	err = s.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error { return s.InsertProject(ctx, tx, want) })
	wantErr(t, "duplicate project", err, ports.ErrAlreadyExists)
}

func testWorkspaces(t *testing.T, s Store) {
	want := workspace(wsA, projA, "default")
	want.OwnerRef = "a " // trailing space is part of the actor reference
	want.OwnerPrincipalUID = "01JD3X5Q8Z9W2V4T6R8P0N2M4K"
	want.OwnerType = "human"
	tx(t, s, func(tx ports.Tx) error { return s.InsertWorkspace(ctx, tx, want) })
	got, err := s.GetWorkspace(ctx, nil, wsA)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("GetWorkspace = %+v, %v\nwant %+v", got, err, want)
	}
	_, err = s.GetWorkspace(ctx, nil, wsB)
	wantErr(t, "missing workspace", err, ports.ErrNotFound)
	// Another workspace with the same prefix key on the same backend is refused,
	// on another backend it is fine.
	dup := workspace(wsB, projA, "default")
	dup.StoragePrefix, dup.StoragePrefixKey = strings.ToUpper(want.StoragePrefix), want.StoragePrefixKey
	err = s.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error { return s.InsertWorkspace(ctx, tx, dup) })
	wantErr(t, "same prefix key on one backend", err, ports.ErrAlreadyExists)
	dup.StorageBackendID = "other"
	tx(t, s, func(tx ports.Tx) error { return s.InsertWorkspace(ctx, tx, dup) })
	if got, err := s.GetWorkspace(ctx, nil, wsB); err != nil || got.StoragePrefix != dup.StoragePrefix || got.OwnerPrincipalUID != "" ||
		got.OwnerType != "" {
		t.Fatalf("workspace on another backend: %+v, %v", got, err)
	}
	err = s.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		w := workspace(wsA, projB, "third")
		return s.InsertWorkspace(ctx, tx, w)
	})
	wantErr(t, "duplicate workspace ID", err, ports.ErrAlreadyExists)
}

func testBranches(t *testing.T, s Store) {
	main := mainBranch(brA, wsA, projA)
	main.CreatedByPrincipalUID = "01JD3X5Q8Z9W2V4T6R8P0N2M4K"
	tx(t, s, func(tx ports.Tx) error { return s.InsertBranch(ctx, tx, main) })
	got, err := s.GetBranch(ctx, nil, brA)
	if err != nil || !reflect.DeepEqual(got, main) {
		t.Fatalf("GetBranch = %+v, %v\nwant %+v", got, err, main)
	}
	if got, err := s.GetBranchByRef(ctx, nil, wsA, "main"); err != nil || got.ID != brA {
		t.Fatalf("GetBranchByRef(main) = %+v, %v", got, err)
	}
	// Refs are compared byte for byte: "main " is not "main".
	_, err = s.GetBranchByRef(ctx, nil, wsA, "main ")
	wantErr(t, `ref "main "`, err, ports.ErrNotFound)
	_, err = s.GetBranchByRef(ctx, nil, wsB, "main")
	wantErr(t, "ref of another workspace", err, ports.ErrNotFound)
	_, err = s.GetBranch(ctx, nil, brB)
	wantErr(t, "missing branch", err, ports.ErrNotFound)
	err = s.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		b := mainBranch(brB, wsA, projA)
		return s.InsertBranch(ctx, tx, b)
	})
	wantErr(t, "second main ref in one workspace", err, ports.ErrAlreadyExists)

	// Listing: ordered by ref bytes, after a ref, limited.
	refs := []string{"main ", "workstreams/W/b", "workstreams/W/a", "workstreams/W/\u4e2d", "workstreams/W/A"}
	tx(t, s, func(tx ports.Tx) error {
		for i, r := range refs {
			b := mainBranch(fmt.Sprintf("01JD3X5Q8Z9W2V4T6R8P0N2C%02d", i), wsA, projA)
			b.RefName, b.Name, b.Type, b.WorkstreamID = r, r[strings.LastIndexByte(r, '/')+1:], "workstream", "01JD3X5Q8Z9W2V4T6R8P0N2WS0"
			if err := s.InsertBranch(ctx, tx, b); err != nil {
				return err
			}
		}
		other := mainBranch(brB, wsB, projA)
		return s.InsertBranch(ctx, tx, other)
	})
	all, err := s.ListBranches(ctx, nil, wsA, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	var gotRefs []string
	for _, b := range all {
		gotRefs = append(gotRefs, b.RefName)
	}
	wantRefs := append([]string{"main"}, refs...)
	sort.Strings(wantRefs)
	if !reflect.DeepEqual(gotRefs, wantRefs) {
		t.Fatalf("ListBranches = %q, want %q", gotRefs, wantRefs)
	}
	page, err := s.ListBranches(ctx, nil, wsA, "main", 2)
	if err != nil || len(page) != 2 || page[0].RefName != "main " || page[1].RefName != "workstreams/W/A" {
		t.Fatalf("ListBranches after main, limit 2 = %+v, %v", page, err)
	}
	if page, err := s.ListBranches(ctx, nil, wsA, "workstreams/W/\u4e2d", 10); err != nil || len(page) != 0 {
		t.Fatalf("ListBranches after the last ref = %+v, %v", page, err)
	}
}

func testBranchCAS(t *testing.T, s Store) {
	tx(t, s, func(tx ports.Tx) error { return s.InsertBranch(ctx, tx, mainBranch(brA, wsA, projA)) })
	cas := func(ws string, gen int64, head string, at int64) bool {
		t.Helper()
		var ok bool
		tx(t, s, func(tx ports.Tx) error {
			var err error
			ok, err = s.UpdateBranchHead(ctx, tx, brA, ws, gen, head, at)
			return err
		})
		return ok
	}
	if !cas(wsA, 1, "01JD3X5Q8Z9W2V4T6R8P0N2S01", 4_000_000) {
		t.Fatal("CAS with the current generation failed")
	}
	b, err := s.GetBranch(ctx, nil, brA)
	if err != nil || b.Generation != 2 || b.HeadSnapshotID != "01JD3X5Q8Z9W2V4T6R8P0N2S01" || b.UpdatedAtUs != 4_000_000 || b.CreatedAtUs != 3_000_001 {
		t.Fatalf("after CAS: %+v, %v", b, err)
	}
	if cas(wsA, 1, "01JD3X5Q8Z9W2V4T6R8P0N2S02", 5_000_000) {
		t.Fatal("CAS with a stale generation succeeded")
	}
	if cas(wsB, 2, "01JD3X5Q8Z9W2V4T6R8P0N2S02", 5_000_000) {
		t.Fatal("CAS with another workspace succeeded")
	}
	if b, _ := s.GetBranch(ctx, nil, brA); b.Generation != 2 || b.HeadSnapshotID != "01JD3X5Q8Z9W2V4T6R8P0N2S01" {
		t.Fatalf("failed CAS changed the branch: %+v", b)
	}
	// A CAS that is rolled back leaves the branch unchanged.
	boom := errors.New("boom")
	err = s.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		ok, err := s.UpdateBranchHead(ctx, tx, brA, wsA, 2, "01JD3X5Q8Z9W2V4T6R8P0N2S03", 6_000_000)
		if err != nil || !ok {
			return fmt.Errorf("CAS: %v %v", ok, err)
		}
		if b, err := s.GetBranch(ctx, tx, brA); err != nil || b.Generation != 3 {
			return fmt.Errorf("the transaction does not see its CAS: %+v %v", b, err)
		}
		return boom
	})
	wantErr(t, "rolled back CAS", err, boom)
	if b, _ := s.GetBranch(ctx, nil, brA); b.Generation != 2 {
		t.Fatalf("rolled back CAS is visible: %+v", b)
	}
}

func snapshot(id string, at int64, branch string, parents ...string) ports.Snapshot {
	return ports.Snapshot{ID: id, WorkspaceID: wsA, ProjectID: projA, BranchID: branch, RefName: ports.MainRefName,
		AuthorRef: "alice", Message: "msg " + id[len(id)-2:], TriggerKind: ports.TriggerManual,
		RootTreeHash: hashOf("root " + id), TreeFormatVersion: 1, ParentIDs: parents, EntryCount: 3, TotalBytes: 42,
		CreatedAtUs: at}
}

func testSnapshots(t *testing.T, s Store) {
	root := snapshot("01JD3X5Q8Z9W2V4T6R8P0N2S01", 10, brA)
	root.ParentIDs = []string{}
	child := snapshot("01JD3X5Q8Z9W2V4T6R8P0N2S02", 20, brA, root.ID)
	child.AuthorRef, child.AuthorPrincipalUID = "01JD3X5Q8Z9W2V4T6R8P0N2M4K", "01JD3X5Q8Z9W2V4T6R8P0N2M4K"
	child.Message = "\u4e2d\u6587 message with trailing space "
	child.TriggerKind = ports.TriggerRestore
	// Actor types, and an actor acted for (a 255-byte reference).
	child.AuthorType, child.OnBehalfOfType = "worker", "human"
	child.OnBehalfOfRef = "@" + strings.Repeat("u", 255-len("@:example.com")) + ":example.com"
	merge := snapshot("01JD3X5Q8Z9W2V4T6R8P0N2S03", 30, "", child.ID, root.ID)
	merge.AuthorType, merge.OnBehalfOfRef = "agent", "manager" // acted for, without a type
	merge.EntryCount, merge.TotalBytes = 1<<40, 1<<52
	tx(t, s, func(tx ports.Tx) error {
		for _, snap := range []ports.Snapshot{root, child, merge} {
			if err := s.InsertSnapshot(ctx, tx, snap); err != nil {
				return err
			}
		}
		return nil
	})
	for _, want := range []ports.Snapshot{root, child, merge} {
		got, err := s.GetSnapshot(ctx, nil, want.ID)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("GetSnapshot(%s) = %+v, %v\nwant %+v", want.ID, got, err, want)
		}
	}
	_, err := s.GetSnapshot(ctx, nil, "01JD3X5Q8Z9W2V4T6R8P0N2S09")
	wantErr(t, "missing snapshot", err, ports.ErrNotFound)
	err = s.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error { return s.InsertSnapshot(ctx, tx, root) })
	wantErr(t, "duplicate snapshot", err, ports.ErrAlreadyExists)
}

func testTimeline(t *testing.T, s Store) {
	// Five snapshots on two branches; two of them in the same microsecond.
	snaps := []ports.Snapshot{
		snapshot("01JD3X5Q8Z9W2V4T6R8P0N2S01", 100, brA),
		snapshot("01JD3X5Q8Z9W2V4T6R8P0N2S02", 200, brB),
		snapshot("01JD3X5Q8Z9W2V4T6R8P0N2S03", 300, brA),
		snapshot("01JD3X5Q8Z9W2V4T6R8P0N2S04", 300, brA),
		snapshot("01JD3X5Q8Z9W2V4T6R8P0N2S05", 400, brB),
	}
	other := snapshot("01JD3X5Q8Z9W2V4T6R8P0N2S06", 500, brA)
	other.WorkspaceID = wsB
	tx(t, s, func(tx ports.Tx) error {
		for _, snap := range append(snaps, other) {
			snap.ParentIDs = []string{}
			if err := s.InsertSnapshot(ctx, tx, snap); err != nil {
				return err
			}
		}
		return nil
	})
	ids := func(q ports.SnapshotQuery) []string {
		t.Helper()
		var out []string
		for {
			page, err := s.ListSnapshots(ctx, nil, q)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range page {
				out = append(out, p.ID[len(p.ID)-2:])
			}
			if len(page) < q.Limit {
				return out
			}
			last := page[len(page)-1]
			q.Before = &ports.TimelineCursor{CreatedAtUs: last.CreatedAtUs, SnapshotID: last.ID}
		}
	}
	for limit := 1; limit <= 6; limit++ {
		if got := strings.Join(ids(ports.SnapshotQuery{WorkspaceID: wsA, Limit: limit}), ","); got != "05,04,03,02,01" {
			t.Fatalf("timeline with limit %d: %s", limit, got)
		}
		if got := strings.Join(ids(ports.SnapshotQuery{WorkspaceID: wsA, BranchID: brA, Limit: limit}), ","); got != "04,03,01" {
			t.Fatalf("branch timeline with limit %d: %s", limit, got)
		}
	}
	if got := ids(ports.SnapshotQuery{WorkspaceID: "01JD3X5Q8Z9W2V4T6R8P0N2WZ0", Limit: 5}); len(got) != 0 {
		t.Fatalf("timeline of an unknown workspace: %v", got)
	}
}

func testBlobs(t *testing.T, s Store) {
	h1, h2, h3 := hashOf("one"), hashOf("two"), hashOf("three")
	blob := func(h contenthash.Hash, size int64) ports.Blob {
		return ports.Blob{WorkspaceID: wsA, Hash: h, Size: size, StorageKey: storagekey.ObjectKey(h), Status: ports.BlobStaged, CreatedAtUs: 7}
	}
	tx(t, s, func(tx ports.Tx) error {
		for _, b := range []ports.Blob{blob(h1, 3), blob(h2, 0), blob(h3, 1<<40)} {
			if err := s.InsertBlob(ctx, tx, b); err != nil {
				return err
			}
		}
		other := blob(h1, 3)
		other.WorkspaceID = wsB
		return s.InsertBlob(ctx, tx, other)
	})
	err := s.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error { return s.InsertBlob(ctx, tx, blob(h1, 3)) })
	wantErr(t, "duplicate blob", err, ports.ErrAlreadyExists)
	if got, err := s.GetBlob(ctx, nil, wsA, h3); err != nil || !reflect.DeepEqual(got, blob(h3, 1<<40)) {
		t.Fatalf("GetBlob = %+v, %v", got, err)
	}
	_, err = s.GetBlob(ctx, nil, wsB, h2)
	wantErr(t, "blob of another workspace", err, ports.ErrNotFound)
	missing := hashOf("missing")
	got, err := s.GetBlobs(ctx, nil, wsA, []contenthash.Hash{h1, missing, h2})
	if err != nil || len(got) != 2 || got[h1].Size != 3 || got[h2].Size != 0 {
		t.Fatalf("GetBlobs = %+v, %v", got, err)
	}
	if got, err := s.GetBlobs(ctx, nil, wsA, nil); err != nil || len(got) != 0 {
		t.Fatalf("GetBlobs(nil) = %+v, %v", got, err)
	}
	// Many hashes at once (batched by the SQL store).
	many := make([]contenthash.Hash, 0, 2500)
	for i := 0; i < 2500; i++ {
		many = append(many, hashOf(fmt.Sprint("many ", i)))
	}
	many = append(many, h3)
	if got, err := s.GetBlobs(ctx, nil, wsA, many); err != nil || len(got) != 1 {
		t.Fatalf("GetBlobs(2501 hashes) = %d rows, %v", len(got), err)
	}
	tx(t, s, func(tx ports.Tx) error { return s.MarkBlobsLive(ctx, tx, wsA, append(many, h1)) })
	for h, want := range map[contenthash.Hash]string{h1: ports.BlobLive, h2: ports.BlobStaged, h3: ports.BlobLive} {
		if b, err := s.GetBlob(ctx, nil, wsA, h); err != nil || b.Status != want {
			t.Fatalf("status of %s: %+v, %v; want %s", h, b, err, want)
		}
	}
	if b, _ := s.GetBlob(ctx, nil, wsB, h1); b.Status != ports.BlobStaged {
		t.Fatalf("MarkBlobsLive changed another workspace: %+v", b)
	}
}

func testIdempotency(t *testing.T, s Store) {
	rec := ports.IdempotencyRecord{ActorRef: "@alice:example.com", Route: "POST /workspaces/{workspace_id}/commits", Key: "k-1",
		RequestHash: strings.Repeat("ab", 32), Status: ports.IdempotencyInProgress, CreatedAtUs: 100, ExpiresAtUs: 200}
	tx(t, s, func(tx ports.Tx) error { return s.InsertIdempotency(ctx, tx, rec) })
	got, err := s.GetIdempotency(ctx, nil, rec.ActorRef, rec.Route, rec.Key)
	if err != nil || got.Status != ports.IdempotencyInProgress || len(got.Response) != 0 || got.RequestHash != rec.RequestHash ||
		got.CreatedAtUs != 100 || got.ExpiresAtUs != 200 || got.ActorType != "" {
		t.Fatalf("GetIdempotency = %+v, %v", got, err)
	}
	// The actor type is kept.
	typed := rec
	typed.Key, typed.ActorType = "k-typed", "worker"
	tx(t, s, func(tx ports.Tx) error { return s.InsertIdempotency(ctx, tx, typed) })
	if got, err := s.GetIdempotency(ctx, nil, typed.ActorRef, typed.Route, typed.Key); err != nil || got.ActorType != "worker" {
		t.Fatalf("typed record = %+v, %v", got, err)
	}
	err = s.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error { return s.InsertIdempotency(ctx, tx, rec) })
	wantErr(t, "duplicate idempotency record", err, ports.ErrAlreadyExists)
	// The same key of another actor (byte for byte) or route is another record.
	tx(t, s, func(tx ports.Tx) error {
		other := rec
		other.ActorRef = "@alice:example.com "
		if err := s.InsertIdempotency(ctx, tx, other); err != nil {
			return err
		}
		other = rec
		other.Route = "POST /projects"
		return s.InsertIdempotency(ctx, tx, other)
	})
	// Completing needs the instance (created_at_us) and an in-progress record.
	err = s.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		return s.CompleteIdempotency(ctx, tx, rec.ActorRef, rec.Route, rec.Key, 99, []byte(`{}`))
	})
	wantErr(t, "complete another instance", err, ports.ErrNotFound)
	response := []byte("{\"snapshot_id\":\"01JD3X5Q8Z9W2V4T6R8P0N2S01\",\"path\":\"\u4e2d\"}")
	tx(t, s, func(tx ports.Tx) error {
		return s.CompleteIdempotency(ctx, tx, rec.ActorRef, rec.Route, rec.Key, 100, response)
	})
	got, err = s.GetIdempotency(ctx, nil, rec.ActorRef, rec.Route, rec.Key)
	if err != nil || got.Status != ports.IdempotencyCompleted || string(got.Response) != string(response) {
		t.Fatalf("completed record = %+v, %v", got, err)
	}
	err = s.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		return s.CompleteIdempotency(ctx, tx, rec.ActorRef, rec.Route, rec.Key, 100, response)
	})
	wantErr(t, "complete a completed record", err, ports.ErrNotFound)
	// Delete only removes the matching instance and status.
	tx(t, s, func(tx ports.Tx) error {
		if err := s.DeleteIdempotency(ctx, tx, rec.ActorRef, rec.Route, rec.Key, 100, ports.IdempotencyInProgress); err != nil {
			return err
		}
		return s.DeleteIdempotency(ctx, tx, rec.ActorRef, rec.Route, rec.Key, 99, ports.IdempotencyCompleted)
	})
	if _, err := s.GetIdempotency(ctx, nil, rec.ActorRef, rec.Route, rec.Key); err != nil {
		t.Fatalf("a non-matching delete removed the record: %v", err)
	}
	tx(t, s, func(tx ports.Tx) error {
		return s.DeleteIdempotency(ctx, tx, rec.ActorRef, rec.Route, rec.Key, 100, ports.IdempotencyCompleted)
	})
	_, err = s.GetIdempotency(ctx, nil, rec.ActorRef, rec.Route, rec.Key)
	wantErr(t, "deleted record", err, ports.ErrNotFound)
	if _, err := s.GetIdempotency(ctx, nil, rec.ActorRef+" ", rec.Route, rec.Key); err != nil {
		t.Fatalf("record of the other actor: %v", err)
	}

	// A request taking over an in-progress record (a delete conditional on
	// creation time and status) while the request that holds it completes
	// it: the delete waits for the completion and then deletes nothing.
	race := rec
	race.Key = "k-race"
	tx(t, s, func(tx ports.Tx) error { return s.InsertIdempotency(ctx, tx, race) })
	locked, release := make(chan struct{}), make(chan struct{})
	completed, deleted := make(chan error, 1), make(chan error, 1)
	go func() {
		completed <- s.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
			if err := s.CompleteIdempotency(ctx, tx, race.ActorRef, race.Route, race.Key, 100, []byte(`{"won":true}`)); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	go func() {
		deleted <- s.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
			return s.DeleteIdempotency(ctx, tx, race.ActorRef, race.Route, race.Key, 100, ports.IdempotencyInProgress)
		})
	}()
	time.Sleep(50 * time.Millisecond) // let the delete wait for the row
	close(release)
	if err := <-completed; err != nil {
		t.Fatalf("completion: %v", err)
	}
	if err := <-deleted; err != nil {
		t.Fatalf("takeover delete: %v", err)
	}
	if got, err := s.GetIdempotency(ctx, nil, race.ActorRef, race.Route, race.Key); err != nil ||
		got.Status != ports.IdempotencyCompleted || string(got.Response) != `{"won":true}` {
		t.Fatalf("after a takeover raced with the completion: %+v %v", got, err)
	}
}

func testOutbox(t *testing.T, s Store) {
	ev := func(id, typ string) ports.Event {
		return ports.Event{ID: id, Type: typ, WorkspaceID: wsA, AggregateID: "01JD3X5Q8Z9W2V4T6R8P0N2S01",
			Payload: []byte("{\"snapshot_id\":\"01JD3X5Q8Z9W2V4T6R8P0N2S01\",\"actor_ref\":\"\u4e2d\"}"), CreatedAtUs: 9}
	}
	tx(t, s, func(tx ports.Tx) error {
		if err := s.Append(ctx, tx, ev("01JD3X5Q8Z9W2V4T6R8P0N2E01", ports.EventSnapshotCreated)); err != nil {
			return err
		}
		return s.Append(ctx, tx, ev("01JD3X5Q8Z9W2V4T6R8P0N2E02", ports.EventBranchUpdated))
	})
	boom := errors.New("boom")
	err := s.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := s.Append(ctx, tx, ev("01JD3X5Q8Z9W2V4T6R8P0N2E03", ports.EventSnapshotRestored)); err != nil {
			return err
		}
		return boom
	})
	wantErr(t, "rolled back append", err, boom)
	err = s.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		return s.Append(ctx, tx, ports.Event{ID: "01JD3X5Q8Z9W2V4T6R8P0N2E04"})
	})
	if err == nil {
		t.Fatal("event without a type accepted")
	}
	events, err := s.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].ID < events[j].ID })
	if len(events) != 2 || events[0].Type != ports.EventSnapshotCreated || events[1].Type != ports.EventBranchUpdated {
		t.Fatalf("events = %+v", events)
	}
	want := ev("01JD3X5Q8Z9W2V4T6R8P0N2E01", ports.EventSnapshotCreated)
	if !reflect.DeepEqual(events[0], want) {
		t.Fatalf("event = %+v\nwant %+v", events[0], want)
	}
}

// testOutboxQueue: the pending events come in commit order (within one transaction, time then
// ID), without events of an earlier version's kinds; a failed delivery
// counts a retry and keeps the event pending; a published event stays
// published and leaves the queue.
func testOutboxQueue(t *testing.T, s Store) {
	ev := func(id, typ string, at int64) ports.Event {
		return ports.Event{ID: id, Type: typ, WorkspaceID: wsA, AggregateID: wsA, Payload: []byte(`{"v":1}`), CreatedAtUs: at}
	}
	// Appended out of order, in two transactions; one row of an old kind.
	tx(t, s, func(tx ports.Tx) error {
		for _, e := range []ports.Event{
			ev("01JD3X5Q8Z9W2V4T6R8P0N2E05", ports.EventBranchUpdated, 21),
			ev("01JD3X5Q8Z9W2V4T6R8P0N2E09", ports.EventCommitCreated, 20),
			ev("01JD3X5Q8Z9W2V4T6R8P0N2E01", "snapshot.created", 5),
		} {
			if err := s.Append(ctx, tx, e); err != nil {
				return err
			}
		}
		return nil
	})
	tx(t, s, func(tx ports.Tx) error {
		if err := s.Append(ctx, tx, ev("01JD3X5Q8Z9W2V4T6R8P0N2E07", ports.EventWorkspaceCreated, 10)); err != nil {
			return err
		}
		return s.Append(ctx, tx, ev("01JD3X5Q8Z9W2V4T6R8P0N2E02", ports.EventBranchCreated, 10))
	})
	ids := func(evs []ports.Event) string {
		var out []string
		for _, e := range evs {
			out = append(out, e.ID[len(e.ID)-2:])
		}
		return strings.Join(out, ",")
	}
	got, err := s.PendingEvents(ctx, 100)
	if err != nil || ids(got) != "09,05,02,07" {
		t.Fatalf("pending = %s, %v; want 09,05,02,07", ids(got), err)
	}
	if n, oldest, err := s.PendingCount(ctx); err != nil || n != 4 || oldest != 10 {
		t.Fatalf("pending count = %d, oldest %d, %v; want 4, 10", n, oldest, err)
	}
	if got, _ := s.PendingEvents(ctx, 2); ids(got) != "09,05" {
		t.Fatalf("pending with limit 2 = %s", ids(got))
	}
	if !reflect.DeepEqual(got[0], ev("01JD3X5Q8Z9W2V4T6R8P0N2E09", ports.EventCommitCreated, 20)) {
		t.Fatalf("pending event = %+v", got[0])
	}
	if err := s.MarkFailed(ctx, "01JD3X5Q8Z9W2V4T6R8P0N2E02", 777); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkFailed(ctx, "01JD3X5Q8Z9W2V4T6R8P0N2E02", 888); err != nil {
		t.Fatal(err)
	}
	if d, err := s.EventDelivery(ctx, "01JD3X5Q8Z9W2V4T6R8P0N2E02"); err != nil || d.Status != ports.EventPending || d.RetryCount != 2 ||
		d.NextRetryAtUs != 888 {
		t.Fatalf("after two failures: %+v %v", d, err)
	}
	if got, _ := s.PendingEvents(ctx, 100); ids(got) != "09,05,02,07" {
		t.Fatalf("a failed event left the queue: %s", ids(got))
	}
	for _, id := range []string{"01JD3X5Q8Z9W2V4T6R8P0N2E02", "01JD3X5Q8Z9W2V4T6R8P0N2E07"} {
		if err := s.MarkPublished(ctx, id, 999); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MarkPublished(ctx, "01JD3X5Q8Z9W2V4T6R8P0N2E07", 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkFailed(ctx, "01JD3X5Q8Z9W2V4T6R8P0N2E07", 5); err != nil {
		t.Fatal(err)
	}
	if d, err := s.EventDelivery(ctx, "01JD3X5Q8Z9W2V4T6R8P0N2E07"); err != nil || d.Status != ports.EventPublished || d.PublishedAtUs != 999 ||
		d.RetryCount != 0 {
		t.Fatalf("published event: %+v %v", d, err)
	}
	if got, _ := s.PendingEvents(ctx, 100); ids(got) != "09,05" {
		t.Fatalf("pending after publishing = %s", ids(got))
	}
	if n, oldest, err := s.PendingCount(ctx); err != nil || n != 2 || oldest != 20 {
		t.Fatalf("pending count after publishing = %d, oldest %d, %v; want 2, 20", n, oldest, err)
	}
	if d, err := s.EventDelivery(ctx, "01JD3X5Q8Z9W2V4T6R8P0N2E01"); err != nil || d.Status != ports.EventPending {
		t.Fatalf("old kind: %+v %v", d, err)
	}
}

func testUsage(t *testing.T, s Store) {
	tx(t, s, func(tx ports.Tx) error { return s.InsertUsage(ctx, tx, wsA, 11) })
	err := s.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error { return s.InsertUsage(ctx, tx, wsA, 12) })
	wantErr(t, "duplicate usage row", err, ports.ErrAlreadyExists)
}

// testConcurrentCAS: of several transactions that move the head of a branch
// from the same generation, exactly one succeeds.
func testConcurrentCAS(t *testing.T, s Store) {
	tx(t, s, func(tx ports.Tx) error { return s.InsertBranch(ctx, tx, mainBranch(brA, wsA, projA)) })
	const n = 8
	results := make(chan error, n)
	wins := make(chan string, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			head := fmt.Sprintf("01JD3X5Q8Z9W2V4T6R8P0N2S%02d", i)
			results <- s.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
				ok, err := s.UpdateBranchHead(ctx, tx, brA, wsA, 1, head, int64(10+i))
				if ok {
					wins <- head
				}
				return err
			})
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	close(wins)
	var won []string
	for h := range wins {
		won = append(won, h)
	}
	b, err := s.GetBranch(ctx, nil, brA)
	if err != nil || len(won) != 1 || b.Generation != 2 || b.HeadSnapshotID != won[0] {
		t.Fatalf("concurrent CAS: winners %v, branch %+v, %v", won, b, err)
	}
}

func testPackedLayout(t *testing.T, s Store) {
	a, b := contenthash.SumBytes([]byte("abc")), contenthash.SumBytes([]byte("def"))
	p := ports.Pack{ID: wsA, Key: "packs/one.pack", Hash: contenthash.SumBytes([]byte("abcdef")), Size: 6}
	packs := []ports.PackWrite{{Pack: p, Chunks: []ports.PackChunk{{Hash: a, Length: 3}, {Hash: b, Offset: 3, Length: 3}}}}
	file := []ports.FileChunk{{Hash: b, Length: 3}, {Hash: a, Offset: 3, Length: 3}}
	h := contenthash.SumBytes([]byte("defabc"))
	tx(t, s, func(tx ports.Tx) error { return s.RecordLayout(ctx, tx, wsA, h, packs, file, 1) })
	rows, err := s.ChunkLocations(ctx, wsA, h, 2, 4)
	if err != nil || len(rows) != 2 || rows[0].FileChunk != file[0] || rows[0].PackOffset != 3 || rows[1].FileChunk != file[1] || rows[1].Pack != p {
		t.Fatalf("file order and pack offsets: %+v %v", rows, err)
	}
	if rows, err := s.ChunkLocations(ctx, wsB, h, 0, 6); err != nil || len(rows) != 0 {
		t.Fatalf("another workspace: %+v %v", rows, err)
	}
}
