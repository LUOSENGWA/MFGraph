package porttest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/storagekey"
)

// Corrupter lets the blob store suite damage a stored object (write other
// bytes under its key, bypassing every check).
type Corrupter interface {
	Corrupt(key string, data []byte)
}

// RunBlobStore runs the contract tests of ports.BlobStore. newStore must
// return an empty store; corrupt damages a stored object.
func RunBlobStore(t *testing.T, newStore func(t *testing.T) (ports.BlobStore, Corrupter)) {
	const prefix = "tenants/00000000000000000000000000/projects/01JD3X5Q8Z9W2V4T6R8P0N2PA0/workspaces/01JD3X5Q8Z9W2V4T6R8P0N2WA0"
	objKey := func(h contenthash.Hash) string { return storagekey.Join(prefix, storagekey.ObjectKey(h)) }
	tmpDir := storagekey.Join(prefix, "objects")
	read := func(t *testing.T, s ports.BlobStore, key string, off, n int64) string {
		t.Helper()
		rc, err := s.Get(ctx, key, off, n)
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

	t.Run("PutIfAbsent", func(t *testing.T) {
		s, _ := newStore(t)
		data := []byte("hello\n")
		h := contenthash.SumBytes(data)
		key := objKey(h)
		created, err := s.PutIfAbsent(ctx, key, bytes.NewReader(data), int64(len(data)), h)
		if err != nil || !created {
			t.Fatalf("first put: %v %v", created, err)
		}
		// An intact object is kept; the reader is not needed.
		created, err = s.PutIfAbsent(ctx, key, errReader{}, int64(len(data)), h)
		if err != nil || created {
			t.Fatalf("second put: %v %v", created, err)
		}
		for _, tc := range []struct {
			off, n int64
			want   string
		}{{0, -1, "hello\n"}, {1, 3, "ell"}, {4, 100, "o\n"}, {6, -1, ""}} {
			if got := read(t, s, key, tc.off, tc.n); got != tc.want {
				t.Errorf("Get(%d, %d) = %q, want %q", tc.off, tc.n, got, tc.want)
			}
		}
		if _, err := s.Get(ctx, key, 7, 1); !errors.Is(err, ports.ErrInvalidRange) {
			t.Errorf("offset past the end: %v", err)
		}
		if info, err := s.Stat(ctx, key); err != nil || info.Size != 6 || info.Key != key {
			t.Errorf("Stat = %+v, %v", info, err)
		}
		for _, missing := range []string{objKey(contenthash.SumBytes([]byte("missing"))), prefix + "/objects/zz"} {
			if _, err := s.Stat(ctx, missing); !errors.Is(err, ports.ErrNotFound) {
				t.Errorf("Stat of a missing key: %v", err)
			}
			if _, err := s.Get(ctx, missing, 0, -1); !errors.Is(err, ports.ErrNotFound) {
				t.Errorf("Get of a missing key: %v", err)
			}
		}
		if err := s.Delete(ctx, key); err != nil {
			t.Fatal(err)
		}
		if err := s.Delete(ctx, key); err != nil {
			t.Errorf("deleting a missing key must succeed: %v", err)
		}
		if _, err := s.Stat(ctx, key); !errors.Is(err, ports.ErrNotFound) {
			t.Error("object still present after Delete")
		}
	})

	t.Run("PutIfAbsentRejectsBadContent", func(t *testing.T) {
		s, _ := newStore(t)
		data := []byte("content")
		h := contenthash.SumBytes(data)
		key := objKey(h)
		if _, err := s.PutIfAbsent(ctx, key, bytes.NewReader(data), int64(len(data)), contenthash.SumBytes([]byte("x"))); !errors.Is(err, ports.ErrHashMismatch) {
			t.Errorf("wrong hash: %v", err)
		}
		if _, err := s.PutIfAbsent(ctx, key, bytes.NewReader(data), 3, h); !errors.Is(err, ports.ErrSizeMismatch) {
			t.Errorf("too much data: %v", err)
		}
		if _, err := s.PutIfAbsent(ctx, key, bytes.NewReader(data), 100, h); !errors.Is(err, ports.ErrSizeMismatch) {
			t.Errorf("too little data: %v", err)
		}
		if _, err := s.Stat(ctx, key); !errors.Is(err, ports.ErrNotFound) {
			t.Errorf("rejected content was stored: %v", err)
		}
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := s.PutIfAbsent(cctx, key, bytes.NewReader(data), int64(len(data)), h); !errors.Is(err, context.Canceled) {
			t.Errorf("canceled: %v", err)
		}
	})

	t.Run("DamagedObjectsAreReplaced", func(t *testing.T) {
		s, corrupt := newStore(t)
		data := []byte("the complete content")
		h := contenthash.SumBytes(data)
		key := objKey(h)
		corrupt.Corrupt(key, data[:5]) // truncated
		created, err := s.PutIfAbsent(ctx, key, bytes.NewReader(data), int64(len(data)), h)
		if err != nil || !created || read(t, s, key, 0, -1) != string(data) {
			t.Fatalf("truncated object not replaced by PutIfAbsent: %v %v", created, err)
		}
		corrupt.Corrupt(key, []byte("x"))
		info, created, err := s.PutStream(ctx, tmpDir, bytes.NewReader(data), 1<<20, contenthash.Hash{}, objKey)
		if err != nil || !created || info.Hash != h || read(t, s, key, 0, -1) != string(data) {
			t.Fatalf("damaged object not replaced by PutStream: %+v %v %v", info, created, err)
		}
		// An object of the right size is kept (no full hash check on a put).
		_, created, err = s.PutStream(ctx, tmpDir, bytes.NewReader(data), 1<<20, h, objKey)
		if err != nil || created {
			t.Fatalf("intact object: %v %v", created, err)
		}
	})

	t.Run("PutStream", func(t *testing.T) {
		s, _ := newStore(t)
		big := bytes.Repeat([]byte("0123456789abcdef"), 1<<14) // 256 KiB
		h := contenthash.SumBytes(big)
		info, created, err := s.PutStream(ctx, tmpDir, bytes.NewReader(big), int64(len(big)), h, objKey)
		if err != nil || !created || info.Hash != h || info.Size != int64(len(big)) || info.Key != objKey(h) {
			t.Fatalf("PutStream = %+v %v %v", info, created, err)
		}
		if got := read(t, s, info.Key, 100, 16); got != string(big[100:116]) {
			t.Errorf("read back %q", got)
		}
		// The same content again: not created.
		if _, created, err := s.PutStream(ctx, tmpDir, bytes.NewReader(big), int64(len(big)), contenthash.Hash{}, objKey); err != nil || created {
			t.Errorf("second PutStream: %v %v", created, err)
		}
		// The empty file.
		info, _, err = s.PutStream(ctx, tmpDir, bytes.NewReader(nil), 10, contenthash.Hash{}, objKey)
		if err != nil || info.Size != 0 || info.Hash != contenthash.SumBytes(nil) {
			t.Errorf("empty file: %+v %v", info, err)
		}
		// Too large and a wrong checksum store nothing.
		other := []byte(strings.Repeat("z", 101))
		if _, _, err := s.PutStream(ctx, tmpDir, bytes.NewReader(other), 100, contenthash.Hash{}, objKey); !errors.Is(err, ports.ErrTooLarge) {
			t.Errorf("101 bytes with a limit of 100: %v", err)
		}
		if _, _, err := s.PutStream(ctx, tmpDir, bytes.NewReader(other), 1000, h, objKey); !errors.Is(err, ports.ErrHashMismatch) {
			t.Errorf("checksum mismatch: %v", err)
		}
		if _, err := s.Stat(ctx, objKey(contenthash.SumBytes(other))); !errors.Is(err, ports.ErrNotFound) {
			t.Errorf("rejected stream was stored: %v", err)
		}
		// A failing reader stores nothing.
		if _, _, err := s.PutStream(ctx, tmpDir, io.MultiReader(bytes.NewReader(other), errReader{}), 1000, contenthash.Hash{}, objKey); err == nil {
			t.Error("a failing reader succeeded")
		}
	})

	t.Run("ConcurrentPuts", func(t *testing.T) {
		s, _ := newStore(t)
		data := []byte("same content")
		h := contenthash.SumBytes(data)
		var wg sync.WaitGroup
		errs := make(chan error, 32)
		for i := 0; i < 16; i++ {
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, err := s.PutIfAbsent(ctx, objKey(h), bytes.NewReader(data), int64(len(data)), h)
				errs <- err
			}()
			go func() {
				defer wg.Done()
				_, _, err := s.PutStream(ctx, tmpDir, bytes.NewReader(data), 100, h, objKey)
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		if got := read(t, s, objKey(h), 0, -1); got != string(data) {
			t.Fatalf("after concurrent puts: %q", got)
		}
	})
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("must not be read") }
