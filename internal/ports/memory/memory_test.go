package memory_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/ids"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/ports/memory"
	"github.com/LUOSENGWA/MFGraph/internal/storagekey"
	"github.com/LUOSENGWA/MFGraph/internal/tree"
)

var ctx = context.Background()

// readAll returns a function so it can be applied to a (ReadCloser, error)
// call directly: readAll(t)(s.Get(...)).
func readAll(t *testing.T) func(io.ReadCloser, error) string {
	return func(rc io.ReadCloser, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		b, err := io.ReadAll(rc)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
}

func TestBlobStorePutGetStat(t *testing.T) {
	s := memory.NewBlobStore()
	data := []byte("hello\n")
	h := contenthash.SumBytes(data)
	key := "ws/" + storagekey.ObjectKey(h)

	created, err := s.PutIfAbsent(ctx, key, bytes.NewReader(data), int64(len(data)), h)
	if err != nil || !created {
		t.Fatalf("first put: %v %v", created, err)
	}
	// Existing object: not created, reader not needed.
	created, err = s.PutIfAbsent(ctx, key, iotaErrReader{}, int64(len(data)), h)
	if err != nil || created {
		t.Fatalf("second put: %v %v", created, err)
	}
	if got := readAll(t)(s.Get(ctx, key, 0, -1)); got != "hello\n" {
		t.Fatalf("Get all = %q", got)
	}
	if got := readAll(t)(s.Get(ctx, key, 1, 3)); got != "ell" {
		t.Fatalf("Get range = %q", got)
	}
	if got := readAll(t)(s.Get(ctx, key, 4, 100)); got != "o\n" {
		t.Fatalf("Get truncated range = %q", got)
	}
	if got := readAll(t)(s.Get(ctx, key, 6, -1)); got != "" {
		t.Fatalf("Get at end = %q", got)
	}
	if _, err := s.Get(ctx, key, 7, 1); !errors.Is(err, ports.ErrInvalidRange) {
		t.Fatalf("offset past end: %v", err)
	}
	info, err := s.Stat(ctx, key)
	if err != nil || info.Size != 6 || info.Hash != h || info.Key != key {
		t.Fatalf("Stat = %+v %v", info, err)
	}
	if _, err := s.Stat(ctx, "missing"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("Stat missing: %v", err)
	}
	if _, err := s.Get(ctx, "missing", 0, -1); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("Get missing: %v", err)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("deleting a missing key must succeed: %v", err)
	}
	if _, err := s.Stat(ctx, key); !errors.Is(err, ports.ErrNotFound) {
		t.Fatal("object still present after Delete")
	}
}

type iotaErrReader struct{}

func (iotaErrReader) Read([]byte) (int, error) { return 0, errors.New("must not be read") }

func TestBlobStoreRejectsBadContent(t *testing.T) {
	s := memory.NewBlobStore()
	data := []byte("content")
	h := contenthash.SumBytes(data)
	if _, err := s.PutIfAbsent(ctx, "k", bytes.NewReader(data), int64(len(data)), contenthash.SumBytes([]byte("other"))); !errors.Is(err, ports.ErrHashMismatch) {
		t.Fatalf("wrong hash: %v", err)
	}
	if _, err := s.PutIfAbsent(ctx, "k", bytes.NewReader(data), 3, h); !errors.Is(err, ports.ErrSizeMismatch) {
		t.Fatalf("too much data: %v", err)
	}
	if _, err := s.PutIfAbsent(ctx, "k", bytes.NewReader(data), 100, h); !errors.Is(err, ports.ErrSizeMismatch) {
		t.Fatalf("too little data: %v", err)
	}
	if len(s.Keys()) != 0 {
		t.Fatal("rejected content was stored")
	}
	if !s.Capabilities().Range || !s.Capabilities().ConditionalPut {
		t.Fatal("capabilities")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.PutIfAbsent(cctx, "k", bytes.NewReader(data), int64(len(data)), h); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
}

func TestBlobStoreConcurrentPuts(t *testing.T) {
	s := memory.NewBlobStore()
	data := []byte("same content")
	h := contenthash.SumBytes(data)
	var wg sync.WaitGroup
	var mu sync.Mutex
	createdCount := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := s.PutIfAbsent(ctx, "k", bytes.NewReader(data), int64(len(data)), h)
			if err != nil {
				t.Error(err)
			}
			if c {
				mu.Lock()
				createdCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if createdCount != 1 {
		t.Fatalf("created %d times, want exactly once", createdCount)
	}
}

func TestTreeStoreRoundTrip(t *testing.T) {
	const ws = "01JD3X5Q8Z9W2V4T6R8P0N2M4K"
	prefix := "tenants/T/projects/P/workspaces/" + ws
	ts, blobs := memory.NewTreeStore(memory.StaticPrefixes(map[string]string{ws: prefix}))
	files := []tree.FileEntry{
		{Path: "docs/a.txt", Hash: contenthash.SumBytes([]byte("a")), Size: 1, Mode: tree.ModeRegular},
		{Path: "bin/tool", Hash: contenthash.SumBytes([]byte("t")), Size: 1, Mode: tree.ModeExecutable},
	}
	root, nodes, err := tree.Build(files)
	if err != nil {
		t.Fatal(err)
	}
	for h, b := range nodes {
		if err := ts.Put(ctx, ws, h, b); err != nil {
			t.Fatal(err)
		}
		if err := ts.Put(ctx, ws, h, b); err != nil { // idempotent
			t.Fatal(err)
		}
	}
	// Stored under tree/<hh>/<hex>.json.zst below the workspace prefix, compressed.
	for h, canonical := range nodes {
		key := storagekey.Join(prefix, storagekey.TreeKey(h))
		z := readAll(t)(blobs.Get(ctx, key, 0, -1))
		dec, _ := zstd.NewReader(nil)
		raw, err := dec.DecodeAll([]byte(z), nil)
		if err != nil || !bytes.Equal(raw, canonical) {
			t.Fatalf("stored object %s is not the zstd of the canonical bytes: %v", key, err)
		}
	}
	// Walking through the store restores the files.
	n := 0
	if err := tree.Walk(ctx, ts.Getter(ws), root, func(p string, e tree.Entry) error {
		if e.Type == tree.TypeFile {
			n++
		}
		return nil
	}); err != nil || n != 2 {
		t.Fatalf("walk: %d files, %v", n, err)
	}
	// Wrong hash is refused; unknown workspace and unknown node are not found.
	if err := ts.Put(ctx, ws, contenthash.SumBytes([]byte("x")), nodes[root]); !errors.Is(err, ports.ErrHashMismatch) {
		t.Fatalf("Put with wrong hash: %v", err)
	}
	if _, err := ts.Get(ctx, "01JD3X5Q8Z9W2V4T6R8P0N2M4A", root); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("unknown workspace: %v", err)
	}
	if _, err := ts.Get(ctx, ws, contenthash.SumBytes([]byte("nope"))); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("unknown node: %v", err)
	}
}

func TestTreeStoreDetectsCorruptionAndBombs(t *testing.T) {
	const ws = "W"
	blobs := memory.NewBlobStore()
	ts, err := memory.NewBlobTreeStore(blobs, memory.StaticPrefixes(map[string]string{ws: "p"}))
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := zstd.NewWriter(nil)
	put := func(h contenthash.Hash, raw []byte) {
		z := enc.EncodeAll(raw, nil)
		if _, err := blobs.PutIfAbsent(ctx, storagekey.Join("p", storagekey.TreeKey(h)), bytes.NewReader(z), int64(len(z)), contenthash.SumBytes(z)); err != nil {
			t.Fatal(err)
		}
	}
	// A stored object whose content does not match its key.
	h := contenthash.SumBytes([]byte(`{"entries":[],"format":1,"kind":"dir"}`))
	put(h, []byte(`{"entries":[],"format":1,"kind":"dir_page"}`))
	if _, err := ts.Get(ctx, ws, h); !errors.Is(err, ports.ErrHashMismatch) {
		t.Fatalf("corrupt node: %v", err)
	}
	// A small object that expands beyond the node limit.
	bomb := []byte(strings.Repeat("a", tree.MaxNodeBytes+1))
	hb := contenthash.SumBytes(bomb)
	put(hb, bomb)
	if _, err := ts.Get(ctx, ws, hb); err == nil {
		t.Fatal("decompression beyond the node limit was accepted")
	}
	if err := ts.Put(ctx, ws, hb, bomb); err == nil {
		t.Fatal("oversized node accepted by Put")
	}
}

func TestAuthenticatorAndAuthorizer(t *testing.T) {
	a := &memory.Authenticator{Tokens: map[string]ports.Principal{"tok": {UID: "U1", TenantID: "T"}}}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer tok")
	if p, err := a.Authenticate(r); err != nil || p.UID != "U1" {
		t.Fatalf("Authenticate = %+v %v", p, err)
	}
	r.Header.Set("Authorization", "Bearer other")
	if _, err := a.Authenticate(r); !errors.Is(err, ports.ErrUnauthenticated) {
		t.Fatalf("bad token: %v", err)
	}

	z := &memory.Authorizer{}
	z.Allow(memory.Grant{PrincipalUID: "U1", WorkspaceID: "WA", Actions: []ports.Action{ports.ActionWorkspaceRead}})
	z.Allow(memory.Grant{PrincipalUID: "U2", ProjectID: "P", Actions: []ports.Action{ports.ActionWorkspaceRead, ports.ActionWorkspaceWrite}})
	u1, u2 := ports.Principal{UID: "U1"}, ports.Principal{UID: "U2"}
	wa := ports.Resource{Type: ports.ResourceSnapshot, ID: "S1", ProjectID: "P", WorkspaceID: "WA"}
	wb := ports.Resource{Type: ports.ResourceSnapshot, ID: "S2", ProjectID: "P", WorkspaceID: "WB"}
	cases := []struct {
		p    ports.Principal
		act  ports.Action
		res  ports.Resource
		want error
	}{
		{u1, ports.ActionWorkspaceRead, wa, nil},
		{u1, ports.ActionWorkspaceWrite, wa, ports.ErrForbidden}, // visible, not allowed
		{u1, ports.ActionWorkspaceRead, wb, ports.ErrNotFound},   // other workspace: hidden
		{u2, ports.ActionWorkspaceWrite, wb, nil},                // project-wide grant
		{ports.Principal{UID: "U3"}, ports.ActionWorkspaceRead, wa, ports.ErrNotFound},
	}
	for i, c := range cases {
		if err := z.Check(ctx, c.p, c.act, c.res); !errors.Is(err, c.want) || (c.want == nil && err != nil) {
			t.Errorf("case %d: got %v, want %v", i, err, c.want)
		}
	}
}

func TestClockAndIDGen(t *testing.T) {
	start := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	c := memory.NewClock(start)
	c.Advance(time.Second)
	if !c.Now().Equal(start.Add(time.Second)) {
		t.Fatal("Advance")
	}
	g := memory.NewIDGen(c)
	a, b := g.NewID(), g.NewID()
	if !ids.Valid(a) || !ids.Valid(b) || !(a < b) {
		t.Fatalf("ids %s %s", a, b)
	}
	g2 := memory.NewIDGen(memory.NewClock(start.Add(time.Second)))
	if g2.NewID() != a {
		t.Fatal("IDGen is not deterministic")
	}
}

func TestOutboxTransactions(t *testing.T) {
	o := &memory.Outbox{}
	ev := func(id string) ports.Event { return ports.Event{ID: id, Type: ports.EventSnapshotCreated} }
	err := o.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		if tx.TxKind() != "memory" {
			t.Fatal("TxKind")
		}
		return o.Append(ctx, tx, ev("E1"))
	})
	if err != nil || len(o.Events()) != 1 {
		t.Fatalf("commit: %v %v", err, o.Events())
	}
	boom := errors.New("boom")
	err = o.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := o.Append(ctx, tx, ev("E2")); err != nil {
			return err
		}
		return boom // rollback: E2 must not become visible
	})
	if !errors.Is(err, boom) || len(o.Events()) != 1 {
		t.Fatalf("rollback: %v %v", err, o.Events())
	}
	var leaked ports.Tx
	_ = o.WithTx(ctx, func(_ context.Context, tx ports.Tx) error { leaked = tx; return nil })
	if err := o.Append(ctx, leaked, ev("E3")); !errors.Is(err, memory.ErrForeignTx) {
		t.Fatalf("finished tx: %v", err)
	}
	other := &memory.Outbox{}
	_ = other.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := o.Append(ctx, tx, ev("E4")); !errors.Is(err, memory.ErrForeignTx) {
			t.Errorf("foreign tx: %v", err)
		}
		return nil
	})
	_ = o.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := o.Append(ctx, tx, ports.Event{}); err == nil {
			t.Error("event without ID accepted")
		}
		return nil
	})
}
