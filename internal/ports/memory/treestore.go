package memory

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/storagekey"
	"github.com/LUOSENGWA/MFGraph/internal/tree"
)

// PrefixResolver returns the immutable storage prefix of a workspace.
type PrefixResolver func(ctx context.Context, workspaceID string) (string, error)

// StaticPrefixes resolves prefixes from a fixed map; unknown workspaces give
// ports.ErrNotFound.
func StaticPrefixes(m map[string]string) PrefixResolver {
	return func(_ context.Context, ws string) (string, error) {
		p, ok := m[ws]
		if !ok {
			return "", fmt.Errorf("%w: workspace %q", ports.ErrNotFound, ws)
		}
		return p, nil
	}
}

// maxCompressedNode bounds the compressed object read back from storage.
const maxCompressedNode = 2 * tree.MaxNodeBytes

// TreeStore is the default ports.TreeStore: nodes are zstd-compressed and
// stored in a BlobStore at <storage_prefix>/tree/<hh>/<hex>.json.zst, where
// <hex> is the hash of the uncompressed canonical bytes. It works with any
// BlobStore (NewTreeStore uses the in-memory one).
type TreeStore struct {
	blobs    ports.BlobStore
	prefixes PrefixResolver
	enc      *zstd.Encoder
	dec      *zstd.Decoder
}

var _ ports.TreeStore = (*TreeStore)(nil)

// NewBlobTreeStore returns a TreeStore on top of blobs.
func NewBlobTreeStore(blobs ports.BlobStore, prefixes PrefixResolver) (*TreeStore, error) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	// Decoded nodes are never larger than tree.MaxNodeBytes. The decoder
	// refuses a frame that declares a larger content size and stops after
	// the first block that takes the output beyond the limit (also for
	// frames without a declared size), so a decompression bomb costs at most
	// the limit plus one block. DecodeAll sizes its output from the frame
	// header; no fixed-capacity destination is needed.
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(tree.MaxNodeBytes))
	if err != nil {
		return nil, err
	}
	return &TreeStore{blobs: blobs, prefixes: prefixes, enc: enc, dec: dec}, nil
}

// NewTreeStore returns a TreeStore over a new in-memory BlobStore.
func NewTreeStore(prefixes PrefixResolver) (*TreeStore, *BlobStore) {
	blobs := NewBlobStore()
	ts, err := NewBlobTreeStore(blobs, prefixes)
	if err != nil {
		panic(err) // only fails on invalid static options
	}
	return ts, blobs
}

func (s *TreeStore) key(ctx context.Context, workspaceID string, h contenthash.Hash) (string, error) {
	prefix, err := s.prefixes(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	return storagekey.Join(prefix, storagekey.TreeKey(h)), nil
}

// Put implements ports.TreeStore.
func (s *TreeStore) Put(ctx context.Context, workspaceID string, h contenthash.Hash, canonical []byte) error {
	if len(canonical) > tree.MaxNodeBytes {
		return fmt.Errorf("memory: tree node of %d bytes exceeds %d", len(canonical), tree.MaxNodeBytes)
	}
	if got := tree.HashOf(canonical); got != h {
		return fmt.Errorf("%w: node %s has hash %s", ports.ErrHashMismatch, h, got)
	}
	key, err := s.key(ctx, workspaceID, h)
	if err != nil {
		return err
	}
	z := s.enc.EncodeAll(canonical, nil)
	_, err = s.blobs.PutIfAbsent(ctx, key, bytes.NewReader(z), int64(len(z)), contenthash.SumBytes(z))
	return err
}

// Get implements ports.TreeStore.
func (s *TreeStore) Get(ctx context.Context, workspaceID string, h contenthash.Hash) ([]byte, error) {
	key, err := s.key(ctx, workspaceID, h)
	if err != nil {
		return nil, err
	}
	rc, err := s.blobs.Get(ctx, key, 0, -1)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	z, err := io.ReadAll(io.LimitReader(rc, maxCompressedNode+1))
	if err != nil {
		return nil, err
	}
	if len(z) > maxCompressedNode {
		return nil, fmt.Errorf("memory: stored tree node %s is too large", h)
	}
	canonical, err := s.dec.DecodeAll(z, nil)
	if err != nil {
		return nil, fmt.Errorf("memory: decompress tree node %s: %w", h, err)
	}
	if len(canonical) > tree.MaxNodeBytes {
		return nil, fmt.Errorf("memory: stored tree node %s decompresses to more than %d bytes", h, tree.MaxNodeBytes)
	}
	if got := tree.HashOf(canonical); got != h {
		return nil, fmt.Errorf("%w: stored node %s decompresses to %s", ports.ErrHashMismatch, h, got)
	}
	return canonical, nil
}

// Getter adapts the store to tree.Getter for one workspace.
func (s *TreeStore) Getter(workspaceID string) tree.Getter {
	return tree.GetterFunc(func(ctx context.Context, h contenthash.Hash) ([]byte, error) {
		return s.Get(ctx, workspaceID, h)
	})
}
