// Package treestore is the default ports.TreeStore: canonical tree nodes,
// zstd-compressed, in any ports.BlobStore at
// <storage_prefix>/tree/<hh>/<hex>.json.zst (content format, section 4).
package treestore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"

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

// CachedPrefixes caches the prefixes resolve finds. A storage prefix never
// changes once a workspace exists, so a cached value never goes stale; the
// cache is emptied when it holds max entries.
func CachedPrefixes(resolve PrefixResolver, max int) PrefixResolver {
	var mu sync.Mutex
	cache := map[string]string{}
	return func(ctx context.Context, ws string) (string, error) {
		mu.Lock()
		p, ok := cache[ws]
		mu.Unlock()
		if ok {
			return p, nil
		}
		p, err := resolve(ctx, ws)
		if err != nil {
			return "", err
		}
		mu.Lock()
		if len(cache) >= max {
			cache = map[string]string{}
		}
		cache[ws] = p
		mu.Unlock()
		return p, nil
	}
}

const (
	// maxCompressedNode bounds the compressed object read back from storage.
	maxCompressedNode = 2 * tree.MaxNodeBytes

	// MaxFrameWindow is the largest zstd window a stored tree node may
	// declare: 8 MiB, up to which RFC 8878 recommends that decoders support
	// windows. The window depends on how the writer was configured, not on
	// the size of the node, so it is limited independently of the output
	// (tree.MaxNodeBytes): a 38-byte node written by a streaming compressor
	// typically declares a window of 2 MiB or more.
	MaxFrameWindow = 8 << 20
)

// Store is the default ports.TreeStore: nodes are zstd-compressed and
// stored in a BlobStore at <storage_prefix>/tree/<hh>/<hex>.json.zst, where
// <hex> is the hash of the uncompressed canonical bytes. It works with any
// BlobStore (the POSIX one in production, the in-memory one in tests).
//
// Put writes one standard zstd frame without a dictionary and with
// Frame_Content_Size in the frame header. Get accepts any frame whose window
// is at most MaxFrameWindow, with or without a content size, and stops
// decompressing at tree.MaxNodeBytes of output; the two limits are
// independent, and exceeding either rejects the object.
type Store struct {
	blobs    ports.BlobStore
	prefixes PrefixResolver
	enc      *zstd.Encoder
	dec      *zstd.Decoder
}

var _ ports.TreeStore = (*Store)(nil)

// New returns a Store on top of blobs.
func New(blobs ports.BlobStore, prefixes PrefixResolver) (*Store, error) {
	// Single-segment frames always carry Frame_Content_Size. Without the
	// option the encoder writes inputs of up to 1024 bytes as frames with a
	// window descriptor, and those leave the content size out below 256
	// bytes (the empty root has 38).
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithSingleSegment(true))
	if err != nil {
		return nil, err
	}
	// Two independent limits. The window: WithDecoderMaxWindow, and
	// WithDecoderMaxMemory at the same value because the decoder also caps
	// the window at the memory limit (with a memory limit of 1 MiB, every
	// frame that declares a larger window would be rejected, whatever the
	// size of the node). The output: DecodeAll decodes into a destination
	// whose capacity is the limit (WithDecodeAllCapLimit), see Get.
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(MaxFrameWindow),
		zstd.WithDecoderMaxMemory(MaxFrameWindow), zstd.WithDecodeAllCapLimit(true))
	if err != nil {
		return nil, err
	}
	return &Store{blobs: blobs, prefixes: prefixes, enc: enc, dec: dec}, nil
}

func (s *Store) key(ctx context.Context, workspaceID string, h contenthash.Hash) (string, error) {
	prefix, err := s.prefixes(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	return storagekey.Join(prefix, storagekey.TreeKey(h)), nil
}

// Put implements ports.TreeStore.
func (s *Store) Put(ctx context.Context, workspaceID string, h contenthash.Hash, canonical []byte) error {
	if len(canonical) > tree.MaxNodeBytes {
		return fmt.Errorf("treestore: tree node of %d bytes exceeds %d", len(canonical), tree.MaxNodeBytes)
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
func (s *Store) Get(ctx context.Context, workspaceID string, h contenthash.Hash) ([]byte, error) {
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
		return nil, fmt.Errorf("treestore: stored tree node %s is too large", h)
	}
	// Check the frame header first: no dictionary, a window of at most
	// MaxFrameWindow and, if the content size is declared, at most
	// tree.MaxNodeBytes. A single-segment frame has no window field; its
	// window is the content size. The decoder repeats these checks for
	// every frame; doing them here gives a clearer error.
	var hdr zstd.Header
	if err := hdr.Decode(z); err != nil {
		return nil, fmt.Errorf("treestore: stored tree node %s is not a zstd frame: %w", h, err)
	}
	window := hdr.WindowSize
	if hdr.SingleSegment {
		window = hdr.FrameContentSize
	}
	switch {
	case hdr.DictionaryID != 0:
		return nil, fmt.Errorf("treestore: stored tree node %s needs a dictionary: %w", h, zstd.ErrUnknownDictionary)
	case window > MaxFrameWindow:
		return nil, fmt.Errorf("treestore: stored tree node %s declares a window of %d bytes, the limit is %d: %w",
			h, window, MaxFrameWindow, zstd.ErrWindowSizeExceeded)
	case hdr.HasFCS && hdr.FrameContentSize > tree.MaxNodeBytes:
		return nil, fmt.Errorf("treestore: stored tree node %s declares %d bytes, the limit is %d: %w",
			h, hdr.FrameContentSize, tree.MaxNodeBytes, zstd.ErrDecoderSizeExceeded)
	}
	// The output limit: DecodeAll fails after the block that takes the output
	// beyond the capacity of the destination (the declared content size, or
	// else tree.MaxNodeBytes), so decoding a decompression bomb stops within
	// one block (at most 128 KiB) of the limit.
	capacity := tree.MaxNodeBytes
	if hdr.HasFCS {
		capacity = int(hdr.FrameContentSize)
	}
	canonical, err := s.dec.DecodeAll(z, make([]byte, 0, capacity))
	if err != nil {
		return nil, fmt.Errorf("treestore: decompress tree node %s: %w", h, err)
	}
	if len(canonical) > tree.MaxNodeBytes {
		return nil, fmt.Errorf("treestore: stored tree node %s decompresses to more than %d bytes", h, tree.MaxNodeBytes)
	}
	if got := tree.HashOf(canonical); got != h {
		return nil, fmt.Errorf("%w: stored node %s decompresses to %s", ports.ErrHashMismatch, h, got)
	}
	return canonical, nil
}

// Getter adapts the store to tree.Getter for one workspace.
func (s *Store) Getter(workspaceID string) tree.Getter {
	return tree.GetterFunc(func(ctx context.Context, h contenthash.Hash) ([]byte, error) {
		return s.Get(ctx, workspaceID, h)
	})
}
