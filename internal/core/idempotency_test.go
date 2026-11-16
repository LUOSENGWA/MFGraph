package core_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/core"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/ports/memory"
)

type gateKey struct{}

// gate stops a request after its first transaction (the insertion of its
// idempotency record), before its business transaction, until released.
type gate struct {
	txs     atomic.Int32
	reached chan struct{}
	release chan struct{}
}

// gatedStore applies gates and records every project it is asked to
// insert (also those whose transaction is rolled back).
type gatedStore struct {
	*memory.Store
	mu       sync.Mutex
	projects []string
}

func (s *gatedStore) WithTx(ctx context.Context, fn func(ctx context.Context, tx ports.Tx) error) error {
	if g, ok := ctx.Value(gateKey{}).(*gate); ok && g.txs.Add(1) == 2 {
		close(g.reached)
		<-g.release
	}
	return s.Store.WithTx(ctx, fn)
}

func (s *gatedStore) InsertProject(ctx context.Context, tx ports.Tx, p ports.Project) error {
	s.mu.Lock()
	s.projects = append(s.projects, p.ID)
	s.mu.Unlock()
	return s.Store.InsertProject(ctx, tx, p)
}

func newGatedEnv(t *testing.T, cfg core.Config) (*env, *gatedStore) {
	gs := &gatedStore{}
	e := newEnvStore(t, cfg, func(m *memory.Store) ports.Store {
		gs.Store = m
		return gs
	})
	return e, gs
}

// TestIdempotencyLease: an in-progress record blocks its key for the
// lease; afterwards a request with the same key takes it over and runs, and
// the request that held the record can no longer complete it, so its
// change is rolled back: the project is created once.
func TestIdempotencyLease(t *testing.T) {
	e, gs := newGatedEnv(t, core.Config{IdempotencyLease: time.Minute})
	req := core.CreateProjectRequest{Name: "leased"}
	g := &gate{reached: make(chan struct{}), release: make(chan struct{})}
	type result struct {
		p   ports.Project
		err error
	}
	first := make(chan result, 1)
	go func() {
		p, err := e.svc.CreateProject(context.WithValue(ctx, gateKey{}, g), alice, req, "k")
		first <- result{p, err}
	}()
	<-g.reached // the first request holds the record

	e.clock.Advance(59 * time.Second)
	_, err := e.svc.CreateProject(ctx, alice, req, "k")
	wantCode(t, "within the lease", err, core.CodeConflict, "reason", core.ReasonInProgress)

	e.clock.Advance(time.Second)
	second, err := e.svc.CreateProject(ctx, alice, req, "k")
	if err != nil {
		t.Fatalf("after the lease: %v", err)
	}
	close(g.release)
	r := <-first
	wantCode(t, "the request whose record was taken over", r.err, core.CodeConflict)

	if len(gs.projects) != 2 {
		t.Fatalf("%d project insertions, want 2 (one rolled back)", len(gs.projects))
	}
	for _, id := range gs.projects {
		_, err := e.store.GetProject(ctx, nil, id)
		if exists := err == nil; exists != (id == second.ID) {
			t.Errorf("project %s exists: %v (the one that took over: %s)", id, exists, second.ID)
		}
	}
	again, err := e.svc.CreateProject(ctx, alice, req, "k")
	if err != nil || again.ID != second.ID {
		t.Fatalf("replay: %+v %v, want project %s", again, err, second.ID)
	}
}

// TestIdempotencyLeaseCompletedMeanwhile: a request that finishes after its
// lease is over, but before another request takes its record over, keeps
// its record; the later request replays it.
func TestIdempotencyLeaseCompletedMeanwhile(t *testing.T) {
	e, gs := newGatedEnv(t, core.Config{IdempotencyLease: time.Second})
	req := core.CreateProjectRequest{Name: "late"}
	p1, err := e.svc.CreateProject(ctx, alice, req, "k")
	e.must(err)
	e.clock.Advance(time.Hour)
	p2, err := e.svc.CreateProject(ctx, alice, req, "k")
	if err != nil || p2.ID != p1.ID || len(gs.projects) != 1 {
		t.Fatalf("replay after the lease: %+v %v (%d insertions)", p2, err, len(gs.projects))
	}
}

// TestIdempotencySameKeyConcurrently: concurrent requests with one key run
// the change once; the others replay it or find it in progress.
func TestIdempotencySameKeyConcurrently(t *testing.T) {
	e, gs := newGatedEnv(t, core.Config{})
	const n = 32
	var wg sync.WaitGroup
	projects := make([]ports.Project, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			projects[i], errs[i] = e.svc.CreateProject(ctx, alice, core.CreateProjectRequest{Name: "once"}, "same")
		}(i)
	}
	wg.Wait()
	ids := map[string]bool{}
	for i := 0; i < n; i++ {
		if errs[i] == nil {
			ids[projects[i].ID] = true
			continue
		}
		wantCode(t, fmt.Sprint("request ", i), errs[i], core.CodeConflict, "reason", core.ReasonInProgress)
	}
	if len(ids) != 1 || len(gs.projects) != 1 {
		t.Fatalf("%d project IDs returned, %d insertions; want 1 and 1", len(ids), len(gs.projects))
	}

	// The same for commits: one snapshot.
	ws := e.workspace(alice)
	h := e.upload(alice, ws.Workspace.ID, "content")
	req := core.CommitRequest{BranchID: ws.MainBranchID, ExpectedGeneration: 1, Message: "m", Changes: []core.Change{put("a.txt", h)}}
	snaps := make([]core.CommitResult, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			snaps[i], errs[i] = e.svc.Commit(ctx, alice, ws.Workspace.ID, req, "commit")
		}(i)
	}
	wg.Wait()
	ids = map[string]bool{}
	for i := 0; i < n; i++ {
		if errs[i] == nil {
			ids[snaps[i].SnapshotID] = true
			continue
		}
		wantCode(t, fmt.Sprint("commit ", i), errs[i], core.CodeConflict, "reason", core.ReasonInProgress)
	}
	timeline, _, err := e.svc.Timeline(ctx, alice, ws.Workspace.ID, ws.MainBranchID, core.Page{Limit: 10})
	e.must(err)
	if len(ids) != 1 || len(timeline) != 1 {
		t.Fatalf("%d snapshot IDs returned, %d snapshots on the branch; want 1 and 1", len(ids), len(timeline))
	}
}

// flakyStore fails the first project insertion with a lock wait timeout.
type flakyStore struct {
	*memory.Store
	failed atomic.Bool
}

func (s *flakyStore) InsertProject(ctx context.Context, tx ports.Tx, p ports.Project) error {
	if s.failed.CompareAndSwap(false, true) {
		return fmt.Errorf("mysql: %w", ports.ErrLockTimeout)
	}
	return s.Store.InsertProject(ctx, tx, p)
}

// TestLockTimeoutIsRetryable: a lock wait timeout is db_unavailable with
// reason lock_timeout, and the same request, with the same key, can be
// repeated at once.
func TestLockTimeoutIsRetryable(t *testing.T) {
	fs := &flakyStore{}
	e := newEnvStore(t, core.Config{}, func(m *memory.Store) ports.Store {
		fs.Store = m
		return fs
	})
	req := core.CreateProjectRequest{Name: "retry"}
	_, err := e.svc.CreateProject(ctx, alice, req, "k")
	wantCode(t, "lock wait timeout", err, core.CodeDBUnavailable, "reason", core.ReasonLockTimeout)
	p, err := e.svc.CreateProject(ctx, alice, req, "k")
	if err != nil || p.Name != "retry" {
		t.Fatalf("repeat with the same key: %+v %v", p, err)
	}
}
