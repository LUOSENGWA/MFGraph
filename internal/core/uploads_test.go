package core_test

import (
	"context"
	"errors"
	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/core"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/ports/memory"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResumableSession(t *testing.T) {
	e := newEnv(t, core.Config{UploadTTL: time.Hour})
	w := e.workspace(alice).Workspace
	init := func(path, key string) core.UploadTicket {
		t.Helper()
		v, err := e.svc.InitUpload(ctx, alice, core.InitUploadRequest{WorkspaceID: w.ID, Path: path, Size: 3}, key)
		e.must(err)
		return v
	}
	ledger := func(res, used int64) {
		t.Helper()
		l, err := e.store.GetUsage(ctx, nil, w.ID)
		if err != nil || l.Reserved != res || l.Used != used {
			t.Fatalf("ledger %+v, %v; want reserved=%d used=%d", l, err, res, used)
		}
	}
	u := init("cafe\u0301/a", "start")
	if again := init("café/a", "start"); again.ID != u.ID {
		t.Fatal("init replay created another session")
	}
	ledger(3, 0)
	_, err := e.svc.UploadStatus(ctx, bob, u.ID)
	wantCode(t, "other actor", err, core.CodeNotFound)
	_, err = e.svc.PutPart(ctx, alice, u.ID, 1, strings.NewReader("four"), -1)
	wantCode(t, "bad size", err, core.CodeInvalidRequest)
	p, err := e.svc.PutPart(ctx, alice, u.ID, 1, strings.NewReader("abc"), 3)
	e.must(err)
	_, err = e.svc.CompleteUpload(ctx, alice, u.ID, []ports.Part{{Number: 1, ETag: "wrong"}}, "")
	wantCode(t, "wrong manifest", err, core.CodeConflict)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			r, err := e.svc.CompleteUpload(ctx, alice, u.ID, []ports.Part{p}, "")
			if err != nil || r.Path != "café/a" || r.Hash != contenthash.SumBytes([]byte("abc")).String() {
				t.Errorf("complete %+v %v", r, err)
			}
		})
	}
	wg.Wait()
	ledger(0, 3)
	second := init("copy", "")
	p, err = e.svc.PutPart(ctx, alice, second.ID, 1, strings.NewReader("abc"), 3)
	e.must(err)
	_, err = e.svc.CompleteUpload(ctx, alice, second.ID, []ports.Part{p}, "")
	e.must(err)
	ledger(0, 3)
	abandoned := init("abandoned", "")
	ledger(3, 3)
	e.clock.Advance(2 * time.Hour)
	e.must(e.svc.ReapUploads(ctx))
	ledger(0, 3)
	_, err = e.svc.UploadStatus(ctx, alice, abandoned.ID)
	wantCode(t, "expired", err, "upload_expired")
	e.clock.Advance(time.Second)
	abort := init("abort", "")
	e.must(e.svc.AbortUpload(ctx, alice, abort.ID))
	e.must(e.svc.AbortUpload(ctx, alice, abort.ID))
	ledger(0, 3)
	branch, err := e.store.GetBranchByRef(ctx, nil, w.ID, ports.MainRefName)
	e.must(err)
	if branch.HeadSnapshotID != "" {
		t.Fatal("completing a session moved the branch")
	}
}

// Simulate a failed final DB write after all bytes were durable. The accepted
// manifest and its reservation survive, and the same completion can retry.
type failUploadResult struct {
	*memory.Store
	fail bool
}

func (s *failUploadResult) UpdateUpload(ctx context.Context, tx ports.Tx, u ports.Upload) error {
	if s.fail && u.Status == ports.UploadCompleted {
		s.fail = false
		return ports.ErrDBUnavailable
	}
	return s.Store.UpdateUpload(ctx, tx, u)
}
func TestRetryFrozenUpload(t *testing.T) {
	e := newEnvStore(t, core.Config{}, func(store *memory.Store) ports.Store { return &failUploadResult{store, true} })
	w := e.workspace(alice).Workspace
	u, err := e.svc.InitUpload(ctx, alice, core.InitUploadRequest{WorkspaceID: w.ID, Path: "a", Size: 3}, "")
	e.must(err)
	p, err := e.svc.PutPart(ctx, alice, u.ID, 1, strings.NewReader("abc"), 3)
	e.must(err)
	_, err = e.svc.CompleteUpload(ctx, alice, u.ID, []ports.Part{p}, "k")
	if !errors.Is(err, ports.ErrDBUnavailable) {
		t.Fatal(err)
	}
	_, err = e.store.GetBlob(ctx, nil, w.ID, contenthash.SumBytes([]byte("abc")))
	if !errors.Is(err, ports.ErrNotFound) {
		t.Fatal("published partial result")
	}
	status, err := e.svc.UploadStatus(ctx, alice, u.ID)
	e.must(err)
	if status.Session.Status != ports.UploadCompleting {
		t.Fatal(status)
	}
	_, err = e.svc.PutPart(ctx, alice, u.ID, 1, strings.NewReader("bad"), 3)
	wantCode(t, "frozen", err, core.CodeConflict)
	_, err = e.svc.CompleteUpload(ctx, alice, u.ID, []ports.Part{p}, "k")
	e.must(err)
}
