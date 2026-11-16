package posix

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
)

// syncRecorder records the directories syncDir is called for.
type syncRecorder struct {
	mu   sync.Mutex
	dirs []string
}

func (r *syncRecorder) sync(dir string) error {
	r.mu.Lock()
	r.dirs = append(r.dirs, dir)
	r.mu.Unlock()
	return syncDirOS(dir)
}

func (r *syncRecorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.dirs
	r.dirs = nil
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// TestExistingObjectsAndDirectoriesAreMadeDurable: a put that finds the
// object, or a directory on its way, already there fsyncs what holds them
// before it returns.
func TestExistingObjectsAndDirectoriesAreMadeDurable(t *testing.T) {
	rec := &syncRecorder{}
	defer func(f func(string) error) { syncDir = f }(syncDir)
	syncDir = rec.sync
	ctx := context.Background()
	data := []byte("content")
	h := contenthash.SumBytes(data)
	key := "ws/objects/" + h.Hex()[:2] + "/" + h.Hex()

	// Another process created the directories of the key (without making
	// them durable) and wrote the object.
	root := t.TempDir()
	objDir := filepath.Join(root, "ws", "objects", h.Hex()[:2])
	if err := os.MkdirAll(objDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(objDir, h.Hex()), data, 0o640); err != nil {
		t.Fatal(err)
	}
	s, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec.take()
	created, err := s.PutIfAbsent(ctx, key, bytes.NewReader(data), int64(len(data)), h)
	if err != nil || created {
		t.Fatalf("put of an existing object: %v %v", created, err)
	}
	got := rec.take()
	for _, d := range []string{objDir, filepath.Dir(objDir), filepath.Join(root, "ws"), root} {
		if !contains(got, d) {
			t.Errorf("existing object: %s not fsynced (fsynced %v)", d, got)
		}
	}
	// The second time only the object's directory is fsynced: the chain is
	// known to be durable.
	if _, _, err := s.PutStream(ctx, "ws/objects", bytes.NewReader(data), 100, h,
		func(contenthash.Hash) string { return key }); err != nil {
		t.Fatal(err)
	}
	if got := rec.take(); len(got) != 1 || got[0] != objDir {
		t.Errorf("second put of an existing object fsynced %v, want only %s", got, objDir)
	}
	// A remembered directory that was removed is created and made durable
	// again.
	if err := os.RemoveAll(objDir); err != nil {
		t.Fatal(err)
	}
	created, err = s.PutIfAbsent(ctx, key, bytes.NewReader(data), int64(len(data)), h)
	if err != nil || !created {
		t.Fatalf("put after the directory was removed: %v %v", created, err)
	}
	if got := rec.take(); !contains(got, filepath.Dir(objDir)) || !contains(got, objDir) {
		t.Errorf("put into a re-created directory fsynced %v", got)
	}
}
