// Package s3 stores workspace objects in an S3-compatible bucket.
package s3

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/s3utils"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

type Config struct {
	Endpoint, Region, Bucket           string
	AccessKey, SecretKey, SessionToken string
}

type Store struct {
	client *minio.Core
	bucket string
	log    *slog.Logger
}

func New(cfg Config, log *slog.Logger) (*Store, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("s3: endpoint must be an HTTP(S) origin")
	}
	if err := s3utils.CheckValidBucketNameStrict(cfg.Bucket); err != nil {
		return nil, fmt.Errorf("s3: invalid bucket: %w", err)
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("s3: access key and secret key are required")
	}
	client, err := minio.NewCore(u.Host, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, cfg.SessionToken),
		Secure: u.Scheme == "https", Region: cfg.Region,
	})
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	return &Store{client: client, bucket: cfg.Bucket, log: log}, nil
}

func (s *Store) Capabilities() ports.Capabilities {
	return ports.Capabilities{Range: true, Copy: true}
}

func storageErr(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	switch minio.ToErrorResponse(err).Code {
	case "NoSuchKey", "NoSuchObject", "NoSuchVersion", "NotFound":
		return fmt.Errorf("%w: %w", ports.ErrNotFound, err)
	case "InvalidRange", "RequestedRangeNotSatisfiable":
		return fmt.Errorf("%w: %w", ports.ErrInvalidRange, err)
	default:
		return fmt.Errorf("%w: %w", ports.ErrStorageUnavailable, err)
	}
}

func (s *Store) Stat(ctx context.Context, key string) (ports.ObjectInfo, error) {
	i, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return ports.ObjectInfo{}, storageErr(err)
	}
	return ports.ObjectInfo{Key: key, Size: i.Size, ETag: i.ETag, ModTime: i.LastModified}, nil
}

func (s *Store) Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	i, err := s.Stat(ctx, key)
	if err != nil {
		return nil, err
	}
	if offset < 0 || offset > i.Size {
		return nil, ports.ErrInvalidRange
	}
	n := i.Size - offset
	if length >= 0 {
		n = min(n, length)
	}
	if n == 0 {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	opts := minio.GetObjectOptions{}
	if err := opts.SetMatchETag(i.ETag); err != nil {
		return nil, err
	}
	if n != i.Size {
		if err := opts.SetRange(offset, offset+n-1); err != nil {
			return nil, err
		}
	}
	body, _, _, err := s.client.GetObject(ctx, s.bucket, key, opts)
	return body, storageErr(err)
}

func (s *Store) Delete(ctx context.Context, key string) error {
	return storageErr(s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}))
}

func (s *Store) intact(ctx context.Context, key string, size int64) (bool, error) {
	i, err := s.Stat(ctx, key)
	if errors.Is(err, ports.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if i.Size == size {
		return true, nil
	}
	s.log.Warn("replacing a damaged stored object", "key", key, "size", i.Size, "expected_size", size)
	return false, nil
}

func (s *Store) PutIfAbsent(ctx context.Context, key string, r io.Reader, size int64, h contenthash.Hash) (bool, error) {
	if h.IsZero() || size < 0 {
		return false, errors.New("s3: missing size or hash")
	}
	if ok, err := s.intact(ctx, key, size); ok || err != nil {
		return false, err
	}
	_, created, err := s.put(ctx, path.Dir(key), r, size, size, h, func(contenthash.Hash) string { return key })
	return created, err
}

func (s *Store) PutStream(ctx context.Context, tmpDir string, r io.Reader, limit int64, expected contenthash.Hash,
	keyOf func(contenthash.Hash) string) (ports.ObjectInfo, bool, error) {
	return s.put(ctx, tmpDir, r, limit, -1, expected, keyOf)
}

// The temporary object stays private until its length and SHA-256 are known.
// Copying within the bucket publishes the verified bytes without a second upload.
func (s *Store) put(ctx context.Context, dir string, r io.Reader, limit, exact int64, expected contenthash.Hash,
	keyOf func(contenthash.Hash) string) (ports.ObjectInfo, bool, error) {
	var zero ports.ObjectInfo
	if err := ctx.Err(); err != nil {
		return zero, false, err
	}
	tmp := strings.TrimSuffix(dir, "/") + "/.tmp-" + rand.Text() + ".part"
	h := contenthash.NewHasher()
	limited := &io.LimitedReader{R: r, N: max(0, limit)}
	// S3 allows at most 10,000 parts. Pick a bounded part buffer that can
	// cover the declared limit up to the server's 5 TiB object maximum.
	partSize := max(int64(8<<20), (min(max(0, limit), int64(5<<40))+9999)/10000)
	// Small objects (including tree nodes) do not need a multipart buffer.
	body := bufio.NewReaderSize(limited, 64<<10)
	head, peekErr := body.Peek(body.Size())
	size := int64(-1)
	if peekErr == io.EOF {
		size = int64(len(head))
	} else if peekErr != nil {
		return zero, false, peekErr
	}
	uploaded, err := s.client.Client.PutObject(ctx, s.bucket, tmp, io.TeeReader(body, h), size,
		minio.PutObjectOptions{PartSize: uint64(partSize), NumThreads: 1})
	if err != nil {
		return zero, false, storageErr(err)
	}
	defer s.removeTemp(ctx, tmp, uploaded.VersionID)
	if limited.N == 0 {
		var probe [1]byte
		n, err := io.ReadFull(r, probe[:])
		if n > 0 {
			if exact >= 0 {
				return zero, false, ports.ErrSizeMismatch
			}
			return zero, false, ports.ErrTooLarge
		}
		if err != io.EOF {
			return zero, false, err
		}
	}
	if exact >= 0 && uploaded.Size != exact {
		return zero, false, ports.ErrSizeMismatch
	}
	sum := h.Sum()
	if !expected.IsZero() && sum != expected {
		return zero, false, ports.ErrHashMismatch
	}
	key := keyOf(sum)
	info := ports.ObjectInfo{Key: key, Size: uploaded.Size, Hash: sum}
	if ok, err := s.intact(ctx, key, info.Size); ok || err != nil {
		return info, false, err
	}
	dst := minio.CopyDestOptions{Bucket: s.bucket, Object: key}
	src := minio.CopySrcOptions{Bucket: s.bucket, Object: tmp, VersionID: uploaded.VersionID}
	if info.Size <= 5<<30 {
		_, err = s.client.Client.CopyObject(ctx, dst, src)
	} else {
		_, err = s.client.ComposeObject(ctx, dst, src)
	}
	return info, err == nil, storageErr(err)
}

// Remove just the temporary version from this call, even on cancellation.
func (s *Store) removeTemp(ctx context.Context, key, version string) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.client.RemoveObject(cleanup, s.bucket, key, minio.RemoveObjectOptions{VersionID: version}); err != nil {
		s.log.Warn("cannot clean temporary S3 object", "key", key, "error", err)
	}
}

// Check only touches a fresh probe object belonging to this invocation.
func (s *Store) Check(ctx context.Context) error {
	key := ".mfgraph-check/" + rand.Text()
	i, err := s.client.Client.PutObject(ctx, s.bucket, key, strings.NewReader("ok"), 2, minio.PutObjectOptions{})
	if err != nil {
		return storageErr(err)
	}
	body, err := s.Get(ctx, key, 1, 1)
	if err == nil {
		b, readErr := io.ReadAll(io.LimitReader(body, 2))
		err = errors.Join(readErr, body.Close())
		if err == nil && string(b) != "k" {
			err = errors.New("range read returned wrong bytes")
		}
	}
	rmErr := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{VersionID: i.VersionID})
	return storageErr(errors.Join(err, rmErr))
}
