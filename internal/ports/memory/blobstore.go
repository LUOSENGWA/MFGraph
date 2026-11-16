package memory

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

type object struct {
	data    []byte
	hash    contenthash.Hash
	modTime time.Time
}

// BlobStore is an in-memory ports.BlobStore.
type BlobStore struct {
	mu      sync.RWMutex
	objects map[string]object
	now     func() time.Time
}

var _ ports.BlobStore = (*BlobStore)(nil)

// NewBlobStore returns an empty store.
func NewBlobStore() *BlobStore {
	return &BlobStore{objects: map[string]object{}, now: time.Now}
}

// Capabilities implements ports.BlobStore.
func (s *BlobStore) Capabilities() ports.Capabilities {
	return ports.Capabilities{Range: true, ConditionalPut: true, List: true}
}

// PutIfAbsent implements ports.BlobStore.
func (s *BlobStore) PutIfAbsent(ctx context.Context, key string, r io.Reader, size int64, h contenthash.Hash) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if key == "" || h.IsZero() || size < 0 {
		return false, fmt.Errorf("memory: invalid put (key %q, size %d)", key, size)
	}
	s.mu.RLock()
	old, exists := s.objects[key]
	s.mu.RUnlock()
	if exists && int64(len(old.data)) == size {
		return false, nil
	}
	// Read at most size+1 bytes to detect oversized input without buffering
	// an unbounded stream.
	data, err := io.ReadAll(io.LimitReader(r, size+1))
	if err != nil {
		return false, err
	}
	if int64(len(data)) != size {
		return false, fmt.Errorf("%w: got at least %d bytes, want %d", ports.ErrSizeMismatch, len(data), size)
	}
	if got := contenthash.SumBytes(data); got != h {
		return false, fmt.Errorf("%w: got %s, want %s", ports.ErrHashMismatch, got, h)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if o, exists := s.objects[key]; exists && int64(len(o.data)) == size { // lost a race with an identical put
		return false, nil
	}
	s.objects[key] = object{data: data, hash: h, modTime: s.now()}
	return true, nil
}

// PutStream implements ports.BlobStore. The in-memory store buffers the
// bytes (at most maxSize+1 of them); tmpDir is not used.
func (s *BlobStore) PutStream(ctx context.Context, _ string, r io.Reader, maxSize int64, expected contenthash.Hash,
	keyOf func(contenthash.Hash) string) (ports.ObjectInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.ObjectInfo{}, false, err
	}
	data, err := io.ReadAll(io.LimitReader(r, maxSize+1))
	if err != nil {
		return ports.ObjectInfo{}, false, err
	}
	if int64(len(data)) > maxSize {
		return ports.ObjectInfo{}, false, fmt.Errorf("%w: more than %d bytes", ports.ErrTooLarge, maxSize)
	}
	h := contenthash.SumBytes(data)
	if !expected.IsZero() && h != expected {
		return ports.ObjectInfo{}, false, fmt.Errorf("%w: got %s, want %s", ports.ErrHashMismatch, h, expected)
	}
	key := keyOf(h)
	info := ports.ObjectInfo{Key: key, Size: int64(len(data)), Hash: h}
	s.mu.Lock()
	defer s.mu.Unlock()
	if o, exists := s.objects[key]; exists && len(o.data) == len(data) {
		info.ModTime = o.modTime
		return info, false, nil
	}
	info.ModTime = s.now()
	s.objects[key] = object{data: data, hash: h, modTime: info.ModTime}
	return info, true, nil
}

// Corrupt replaces the bytes stored under key without any check (test
// helper for damaged objects).
func (s *BlobStore) Corrupt(key string, data []byte) {
	s.mu.Lock()
	s.objects[key] = object{data: append([]byte(nil), data...), modTime: s.now()}
	s.mu.Unlock()
}

// Get implements ports.BlobStore.
func (s *BlobStore) Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	o, ok := s.objects[key]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: object %q", ports.ErrNotFound, key)
	}
	size := int64(len(o.data))
	if offset < 0 || offset > size {
		return nil, fmt.Errorf("%w: offset %d, size %d", ports.ErrInvalidRange, offset, size)
	}
	end := size
	if length >= 0 && offset+length < size {
		end = offset + length
	}
	return io.NopCloser(bytes.NewReader(o.data[offset:end])), nil
}

// Stat implements ports.BlobStore.
func (s *BlobStore) Stat(ctx context.Context, key string) (ports.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return ports.ObjectInfo{}, err
	}
	s.mu.RLock()
	o, ok := s.objects[key]
	s.mu.RUnlock()
	if !ok {
		return ports.ObjectInfo{}, fmt.Errorf("%w: object %q", ports.ErrNotFound, key)
	}
	return ports.ObjectInfo{Key: key, Size: int64(len(o.data)), Hash: o.hash, ModTime: o.modTime}, nil
}

// Delete implements ports.BlobStore.
func (s *BlobStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.objects, key)
	s.mu.Unlock()
	return nil
}

// Keys returns the stored keys (test helper).
func (s *BlobStore) Keys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.objects))
	for k := range s.objects {
		keys = append(keys, k)
	}
	return keys
}
