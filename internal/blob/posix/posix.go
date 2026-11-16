// Package posix is a ports.BlobStore on a local (or NFS) file system: every
// object is a file at <root>/<key>.
//
// Durability: an object is written to a temporary file on the same file
// system (created with O_EXCL), fsynced, renamed to its final name, and
// the directory is fsynced; a put returns only then. A reader therefore
// sees either no object or the complete one. Objects are content-addressed,
// so an existing file is kept if it has the expected size; a file of
// another size (a truncated or damaged object) is replaced atomically by
// the new bytes, and a warning is logged. A full hash check of stored
// objects is a background audit, not part of a put.
package posix

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

const (
	dirPerm  = 0o750
	filePerm = 0o640
	// tmpPattern names temporary files: a leading dot and a suffix that no
	// object key has (object keys end in 64 hex digits or ".json.zst").
	tmpPattern = ".tmp-*.part"
)

// Store is a BlobStore rooted at a directory.
type Store struct {
	root string
	log  *slog.Logger
}

var _ ports.BlobStore = (*Store)(nil)

// New returns a store rooted at the existing directory root. It checks that
// the directory is writable by creating and removing a temporary file.
func New(root string, log *slog.Logger) (*Store, error) {
	if log == nil {
		log = slog.Default()
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("%w: storage root: %v", ports.ErrStorageUnavailable, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%w: storage root %s is not a directory", ports.ErrStorageUnavailable, abs)
	}
	f, err := os.CreateTemp(abs, tmpPattern)
	if err != nil {
		return nil, fmt.Errorf("%w: storage root is not writable: %v", ports.ErrStorageUnavailable, err)
	}
	name := f.Name()
	f.Close()
	if err := os.Remove(name); err != nil {
		return nil, err
	}
	return &Store{root: abs, log: log}, nil
}

// Root returns the absolute root directory.
func (s *Store) Root() string { return s.root }

// Check reports whether the root directory is still usable (readiness).
func (s *Store) Check(context.Context) error {
	fi, err := os.Stat(s.root)
	if err != nil {
		return fmt.Errorf("%w: %v", ports.ErrStorageUnavailable, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%w: storage root is not a directory", ports.ErrStorageUnavailable)
	}
	return nil
}

// Capabilities implements ports.BlobStore.
func (s *Store) Capabilities() ports.Capabilities {
	return ports.Capabilities{Range: true, AtomicRename: true}
}

// path maps a key to a file name. Keys are '/'-separated relative keys
// (a storage prefix and a key of package storagekey); segments that are
// empty, "." or "..", and backslashes or NUL bytes are refused, so a key
// can never name a file outside the root.
func (s *Store) path(key string) (string, error) {
	if key == "" || strings.ContainsAny(key, "\\\x00") || key[0] == '/' {
		return "", fmt.Errorf("posix: invalid key %q", key)
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("posix: invalid key %q", key)
		}
	}
	return filepath.Join(s.root, filepath.FromSlash(key)), nil
}

// syncDir fsyncs a directory, so that a rename into it is durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

// mkdirAll creates dir and fsyncs the parents of the directories it
// creates, so that the new directory entries are durable too.
func mkdirAll(dir string) error {
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		return nil
	}
	parent := filepath.Dir(dir)
	if parent != dir {
		if err := mkdirAll(parent); err != nil {
			return err
		}
	}
	if err := os.Mkdir(dir, dirPerm); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return syncDir(parent)
}

// existing reports whether an intact object (a regular file of size bytes)
// is stored at path. A file of another size, or one that cannot be
// examined, is logged as damaged; the caller replaces it.
func (s *Store) existing(key, path string, size int64) bool {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false
	case err != nil:
		s.log.Warn("replacing a stored object that cannot be examined", "key", key, "err", err.Error())
		return false
	case !fi.Mode().IsRegular():
		s.log.Warn("replacing a stored object that is not a regular file", "key", key, "mode", fi.Mode().String())
		return false
	case fi.Size() != size:
		s.log.Warn("replacing a damaged stored object", "key", key, "size", fi.Size(), "expected_size", size)
		return false
	}
	return true
}

// commit makes the temporary file tmp durable under path.
func commit(tmp *os.File, path string) error {
	if err := tmp.Chmod(filePerm); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := mkdirAll(dir); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return syncDir(dir)
}

// discard removes a temporary file that is not used.
func discard(tmp *os.File) {
	tmp.Close()
	os.Remove(tmp.Name())
}

// ctxReader stops reading when the context is canceled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// PutIfAbsent implements ports.BlobStore.
func (s *Store) PutIfAbsent(ctx context.Context, key string, r io.Reader, size int64, h contenthash.Hash) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if h.IsZero() || size < 0 {
		return false, fmt.Errorf("posix: invalid put (key %q, size %d)", key, size)
	}
	path, err := s.path(key)
	if err != nil {
		return false, err
	}
	if s.existing(key, path, size) {
		return false, nil
	}
	dir := filepath.Dir(path)
	if err := mkdirAll(dir); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(dir, tmpPattern)
	if err != nil {
		return false, err
	}
	hasher := contenthash.NewHasher()
	n, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(ctxReader{ctx, r}, size+1))
	switch {
	case err != nil:
		discard(tmp)
		return false, err
	case n != size:
		discard(tmp)
		return false, fmt.Errorf("%w: got %d bytes or more, want %d", ports.ErrSizeMismatch, n, size)
	case hasher.Sum() != h:
		discard(tmp)
		return false, fmt.Errorf("%w: got %s, want %s", ports.ErrHashMismatch, hasher.Sum(), h)
	}
	if err := commit(tmp, path); err != nil {
		os.Remove(tmp.Name())
		return false, err
	}
	return true, nil
}

// PutStream implements ports.BlobStore. The temporary file is created in
// the directory of tmpDir (for content objects the workspace's objects/
// directory, on the same file system as the final object) and renamed to
// the object's key once the hash is known.
func (s *Store) PutStream(ctx context.Context, tmpDir string, r io.Reader, maxSize int64, expected contenthash.Hash,
	keyOf func(contenthash.Hash) string) (ports.ObjectInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.ObjectInfo{}, false, err
	}
	dir, err := s.path(tmpDir)
	if err != nil {
		return ports.ObjectInfo{}, false, err
	}
	if err := mkdirAll(dir); err != nil {
		return ports.ObjectInfo{}, false, err
	}
	tmp, err := os.CreateTemp(dir, tmpPattern)
	if err != nil {
		return ports.ObjectInfo{}, false, err
	}
	hasher := contenthash.NewHasher()
	n, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(ctxReader{ctx, r}, maxSize+1))
	if err != nil {
		discard(tmp)
		return ports.ObjectInfo{}, false, err
	}
	if n > maxSize {
		discard(tmp)
		return ports.ObjectInfo{}, false, fmt.Errorf("%w: more than %d bytes", ports.ErrTooLarge, maxSize)
	}
	h := hasher.Sum()
	if !expected.IsZero() && h != expected {
		discard(tmp)
		return ports.ObjectInfo{}, false, fmt.Errorf("%w: got %s, want %s", ports.ErrHashMismatch, h, expected)
	}
	key := keyOf(h)
	path, err := s.path(key)
	if err != nil {
		discard(tmp)
		return ports.ObjectInfo{}, false, err
	}
	info := ports.ObjectInfo{Key: key, Size: n, Hash: h}
	if s.existing(key, path, n) {
		discard(tmp)
		return info, false, nil
	}
	if err := commit(tmp, path); err != nil {
		os.Remove(tmp.Name())
		return ports.ObjectInfo{}, false, err
	}
	return info, true, nil
}

// readCloser reads a section of a file and closes the file.
type readCloser struct {
	io.Reader
	f *os.File
}

func (r readCloser) Close() error { return r.f.Close() }

// Get implements ports.BlobStore.
func (s *Store) Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := s.path(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: object %q", ports.ErrNotFound, key)
	}
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	size := fi.Size()
	if offset < 0 || offset > size {
		f.Close()
		return nil, fmt.Errorf("%w: offset %d, size %d", ports.ErrInvalidRange, offset, size)
	}
	n := size - offset
	if length >= 0 && length < n {
		n = length
	}
	return readCloser{Reader: io.NewSectionReader(f, offset, n), f: f}, nil
}

// Stat implements ports.BlobStore.
func (s *Store) Stat(ctx context.Context, key string) (ports.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return ports.ObjectInfo{}, err
	}
	path, err := s.path(key)
	if err != nil {
		return ports.ObjectInfo{}, err
	}
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ports.ObjectInfo{}, fmt.Errorf("%w: object %q", ports.ErrNotFound, key)
	}
	if err != nil {
		return ports.ObjectInfo{}, err
	}
	if !fi.Mode().IsRegular() {
		return ports.ObjectInfo{}, fmt.Errorf("%w: object %q is not a regular file", ports.ErrNotFound, key)
	}
	return ports.ObjectInfo{Key: key, Size: fi.Size(), ModTime: fi.ModTime()}, nil
}

// Delete implements ports.BlobStore.
func (s *Store) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return syncDir(filepath.Dir(path))
}
