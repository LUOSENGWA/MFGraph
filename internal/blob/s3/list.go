package s3

import (
	"context"

	"github.com/minio/minio-go/v7"

	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

// Walk uses a terminated prefix so similarly named sibling directories stay
// outside the result. The caller may stop the listing on the first error.
func (s *Store) Walk(ctx context.Context, dir string, visit func(ports.ObjectInfo) error) error {
	for obj := range s.client.Client.ListObjectsIter(ctx, s.bucket, minio.ListObjectsOptions{Prefix: dir + "/", Recursive: true}) {
		if obj.Err != nil {
			return storageErr(obj.Err)
		}
		if err := visit(ports.ObjectInfo{Key: obj.Key, Size: obj.Size, ETag: obj.ETag, ModTime: obj.LastModified, VersionID: obj.VersionID}); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// DeleteVersion removes the observed version, including the null version on
// an unversioned bucket when the service reports one.
func (s *Store) DeleteVersion(ctx context.Context, key, version string) error {
	return storageErr(s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{VersionID: version}))
}

// DeleteDirectoryVersions also works for suspended versioning, where a
// current plain GET can hide older data behind a delete marker.
func (s *Store) DeleteDirectoryVersions(ctx context.Context, dir string) error {
	opts := minio.ListObjectsOptions{Prefix: dir + "/", Recursive: true, WithVersions: true}
	for obj := range s.client.Client.ListObjectsIter(ctx, s.bucket, opts) {
		if obj.Err != nil {
			if notImplemented(obj.Err) && !s.versioned.Load() {
				return s.Walk(ctx, dir, func(i ports.ObjectInfo) error { return s.Delete(ctx, i.Key) })
			}
			return storageErr(obj.Err)
		}
		if err := s.DeleteVersion(ctx, obj.Key, obj.VersionID); err != nil {
			return err
		}
	}
	return ctx.Err()
}
