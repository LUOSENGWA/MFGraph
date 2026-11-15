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
	_, exists := s.objects[key]
	s.mu.RUnlock()
	if exists {
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
	if _, exists := s.objects[key]; exists { // lost a race with an identical put
		return false, nil
	}
	s.objects[key] = object{data: data, hash: h, modTime: s.now()}
	return true, nil
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
