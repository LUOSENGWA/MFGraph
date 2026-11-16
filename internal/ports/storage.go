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
	// ErrTooLarge: a streamed object exceeds the size limit.
	ErrTooLarge = errors.New("object too large")
	// ErrStorageUnavailable: the storage backend cannot be used (for
	// example, its root directory is missing). The API answers 503
	// storage_unavailable.
	ErrStorageUnavailable = errors.New("storage unavailable")
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

// ReadLimited reads r up to and including its end, as long as that is at
// most limit bytes: it reads limit bytes and then probes for one more byte.
// over reports that r had more (never computing limit+1, which overflows
// for the largest int64). Implementations of BlobStore use it to enforce
// sizes on readers that may be longer than announced.
func ReadLimited(r io.Reader, w io.Writer, limit int64) (n int64, over bool, err error) {
	if limit < 0 {
		limit = 0
	}
	n, err = io.Copy(w, io.LimitReader(r, limit))
	if err != nil || n < limit {
		return n, false, err
	}
	var probe [1]byte
	k, err := io.ReadFull(r, probe[:])
	switch {
	case k > 0:
		return n, true, nil
	case err == io.EOF:
		return n, false, nil
	}
	return n, false, err
}

// BlobStore stores immutable objects under full keys
// (workspace storage_prefix + "/" + relative key; see package storagekey).
//
// Durability: PutIfAbsent and PutStream return only after the object is
// durable. The POSIX implementation writes a temporary file on the same file
// system, fsyncs it, renames it to the final name and fsyncs the
// directory; an S3 PUT is durable when it succeeds.
//
// Objects are content-addressed, so an existing object under a key is
// expected to hold the right bytes. Both put methods check its size: an
// object of the expected size is kept (a full hash check is a background
// audit, not part of a put); an object of another size, or one that cannot
// be examined, is treated as damaged and atomically replaced by the new
// bytes, with a warning in the log.
type BlobStore interface {
	Capabilities() Capabilities

	// PutIfAbsent stores the size bytes read from r under key. The SHA-256 of
	// the bytes must equal h and their number must equal size, otherwise
	// nothing is stored and ErrHashMismatch / ErrSizeMismatch is returned. If
	// an intact object (of this size) already exists under key, it is left
	// untouched, r need not be read, and created is false.
	PutIfAbsent(ctx context.Context, key string, r io.Reader, size int64, h contenthash.Hash) (created bool, err error)

	// PutStream stores the bytes read from r, to its end, under the key
	// keyOf returns for their hash. The bytes are hashed while they are
	// written to a temporary object below the key prefix tmpDir (on the same
	// backend, so that they are written once and then moved to the key),
	// and memory use does not grow with the size. More than maxSize bytes
	// give ErrTooLarge; a hash other than expected (if expected is not zero)
	// gives ErrHashMismatch; in both cases nothing is stored. If an intact
	// object exists under the key, the temporary object is discarded and
	// created is false. info has the key, size and hash of the object.
	PutStream(ctx context.Context, tmpDir string, r io.Reader, maxSize int64, expected contenthash.Hash,
		keyOf func(contenthash.Hash) string) (info ObjectInfo, created bool, err error)

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
// implementation (package treestore) keeps zstd-compressed nodes in a
// BlobStore under tree/<hh>/<hex>.json.zst; the hash is always that of the
// uncompressed canonical bytes, so the compression can change without
// changing hashes.
type TreeStore interface {
	// Put stores canonical bytes whose SHA-256 must equal h. Storing an
	// existing node is a no-op.
	Put(ctx context.Context, workspaceID string, h contenthash.Hash, canonical []byte) error
	// Get returns the canonical bytes of node h, verified against h, or
	// ErrNotFound.
	Get(ctx context.Context, workspaceID string, h contenthash.Hash) ([]byte, error)
}
