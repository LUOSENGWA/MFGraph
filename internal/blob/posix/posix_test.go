package posix_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/blob/posix"
	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/ports/porttest"
)

// corrupter writes bytes directly into the file of a key.
type corrupter struct{ root string }

func (c corrupter) Corrupt(key string, data []byte) {
	p := filepath.Join(c.root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		panic(err)
	}
	if err := os.WriteFile(p, data, 0o640); err != nil {
		panic(err)
	}
}

func newStore(t *testing.T, log *slog.Logger) (*posix.Store, string) {
	t.Helper()
	root := t.TempDir()
	s, err := posix.New(root, log)
	if err != nil {
		t.Fatal(err)
	}
	return s, s.Root()
}

func TestBlobStoreContract(t *testing.T) {
	porttest.RunBlobStore(t, func(t *testing.T) (ports.BlobStore, porttest.Corrupter) {
		s, root := newStore(t, slog.New(slog.NewTextHandler(io.Discard, nil)))
		return s, corrupter{root}
	})
}

// TestDurableLayout: objects are files at <root>/<key>, no temporary file
// is left behind, and a damaged object is replaced with a warning.
func TestDurableLayout(t *testing.T) {
	var logs bytes.Buffer
	s, root := newStore(t, slog.New(slog.NewTextHandler(&logs, nil)))
	ctx := context.Background()
	data := []byte("hello\n")
	h := contenthash.SumBytes(data)
	key := "ws/objects/" + h.Hex()[:2] + "/" + h.Hex()
	if _, _, err := s.PutStream(ctx, "ws/objects", bytes.NewReader(data), 100, contenthash.Hash{},
		func(contenthash.Hash) string { return key }); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "ws", "objects", h.Hex()[:2], h.Hex()))
	if err != nil || string(b) != "hello\n" {
		t.Fatalf("object file: %q %v", b, err)
	}
	var tmp []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if strings.HasPrefix(d.Name(), ".tmp-") {
			tmp = append(tmp, p)
		}
		return nil
	})
	if len(tmp) != 0 {
		t.Fatalf("temporary files left: %v", tmp)
	}
	corrupter{root}.Corrupt(key, []byte("hel"))
	if created, err := s.PutIfAbsent(ctx, key, bytes.NewReader(data), int64(len(data)), h); err != nil || !created {
		t.Fatalf("replace damaged: %v %v", created, err)
	}
	if !strings.Contains(logs.String(), "replacing a damaged stored object") {
		t.Errorf("no warning logged: %s", logs.String())
	}
	if fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(key))); err != nil || fi.Mode().Perm() != 0o640 {
		t.Errorf("object mode: %v %v", fi.Mode(), err)
	}
}

func TestKeysStayBelowTheRoot(t *testing.T) {
	s, _ := newStore(t, nil)
	ctx := context.Background()
	for _, key := range []string{"", "/abs", "a/../b", "a//b", "a/./b", `a\b`, "a\x00b", "..", "a/"} {
		if _, err := s.Stat(ctx, key); err == nil || strings.Contains(err.Error(), "not found") {
			t.Errorf("key %q: %v", key, err)
		}
	}
	if _, err := posix.New(filepath.Join(t.TempDir(), "missing"), nil); err == nil {
		t.Error("a missing root was accepted")
	}
	f := filepath.Join(t.TempDir(), "file")
	os.WriteFile(f, nil, 0o600)
	if _, err := posix.New(f, nil); err == nil {
		t.Error("a file as root was accepted")
	}
	if err := s.Check(ctx); err != nil {
		t.Error(err)
	}
}

func TestReadinessAfterRootBecomesReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write a mode-0555 directory")
	}
	s, root := newStore(t, nil)
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o750) })
	if err := s.Check(context.Background()); !errors.Is(err, ports.ErrStorageUnavailable) {
		t.Fatalf("read-only root: %v", err)
	}
}
