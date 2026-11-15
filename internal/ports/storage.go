package ports

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
)

var (
	// ErrHashMismatch: written content does not hash to the declared value.
	ErrHashMismatch = errors.New("content hash mismatch")
	// ErrSizeMismatch: written content does not have the declared size.
	ErrSizeMismatch = errors.New("content size mismatch")
	// ErrInvalidRange: a read range starts beyond the end of the object.
	ErrInvalidRange = errors.New("invalid range")
)

// Capabilities describes what a BlobStore backend supports natively. Callers
// must not assume that backends behave alike; missing capabilities are
// emulated or the feature is unavailable.
type Capabilities struct {
	Range          bool // Get with offset/length without reading the prefix
	ConditionalPut bool // native put-if-absent (otherwise check-then-write)
	AtomicRename   bool // POSIX rename within a directory
	Multipart      bool // multipart uploads (later slice)
	Presign        bool // presigned URLs (later slice)
	List           bool // prefix listing, needed by garbage collection (later slice)
	Copy           bool // server-side copy
	Versioning     bool // object versioning is enabled on the bucket
}

// ObjectInfo is the metadata of a stored object.
type ObjectInfo struct {
	Key     string
	Size    int64
	Hash    contenthash.Hash // zero when the backend does not know it
	ETag    string           // backend ETag, if any
	ModTime time.Time
}

// BlobStore stores immutable objects under full keys
// (workspace storage_prefix + "/" + relative key; see package storagekey).
//
// Durability: PutIfAbsent returns only after the object is durable. The POSIX
// implementation must write a temporary file in the destination directory,
// fsync the file, rename it to the final name, and fsync the directory; an S3
// PUT is durable when it succeeds.
type BlobStore interface {
	Capabilities() Capabilities

	// PutIfAbsent stores the size bytes read from r under key. The SHA-256 of
	// the bytes must equal h and their number must equal size, otherwise
	// nothing is stored and ErrHashMismatch / ErrSizeMismatch is returned. If
	// an object already exists under key, it is left untouched, r need not be
	// read, and created is false.
	PutIfAbsent(ctx context.Context, key string, r io.Reader, size int64, h contenthash.Hash) (created bool, err error)

	// Get reads length bytes starting at offset; length < 0 reads to the end,
	// and a range past the end is truncated. offset > size gives
	// ErrInvalidRange; a missing key gives ErrNotFound.
	Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error)

	// Stat returns the object metadata or ErrNotFound.
	Stat(ctx context.Context, key string) (ObjectInfo, error)

	// Delete removes the object; deleting a missing key succeeds. Not used by
	// the first core slice (garbage collection comes later).
	Delete(ctx context.Context, key string) error
}

// TreeStore stores canonical tree nodes per workspace. The default
// implementation keeps zstd-compressed nodes in a BlobStore under
// tree/<hh>/<hex>.json.zst; the hash is always that of the uncompressed
// canonical bytes, so the compression can change without changing hashes.
type TreeStore interface {
	// Put stores canonical bytes whose SHA-256 must equal h. Storing an
	// existing node is a no-op.
	Put(ctx context.Context, workspaceID string, h contenthash.Hash, canonical []byte) error
	// Get returns the canonical bytes of node h, verified against h, or
	// ErrNotFound.
	Get(ctx context.Context, workspaceID string, h contenthash.Hash) ([]byte, error)
}
