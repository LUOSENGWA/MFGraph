package s3

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"

	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/ports/porttest"
)

type fixture struct {
	*Store
	t *testing.T
}

func (f fixture) Corrupt(key string, data []byte) {
	f.t.Helper()
	_, err := f.client.Client.PutObject(context.Background(), f.bucket, key, bytes.NewReader(data),
		int64(len(data)), minio.PutObjectOptions{})
	if err != nil {
		f.t.Fatal(err)
	}
}

func testStore(t *testing.T) fixture {
	t.Helper()
	endpoint := os.Getenv("MFGRAPH_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set MFGRAPH_TEST_S3_ENDPOINT for S3 contract tests")
	}
	ctx := context.Background()
	cfg := Config{Endpoint: endpoint, Bucket: "mfgraph-test-" + strings.ToLower(rand.Text()), Region: "us-east-1",
		AccessKey: os.Getenv("MFGRAPH_TEST_S3_ACCESS_KEY"), SecretKey: os.Getenv("MFGRAPH_TEST_S3_SECRET_KEY")}
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: cfg.Region}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for obj := range s.client.Client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Recursive: true}) {
			if obj.Err != nil {
				t.Error(obj.Err)
				break
			}
			if err := s.Delete(ctx, obj.Key); err != nil {
				t.Error(err)
			}
		}
		if err := s.client.RemoveBucket(ctx, s.bucket); err != nil {
			t.Error(err)
		}
	})
	return fixture{s, t}
}

func TestS3BlobStoreContract(t *testing.T) {
	porttest.RunBlobStore(t, func(t *testing.T) (ports.BlobStore, porttest.Corrupter) {
		f := testStore(t)
		return f.Store, f
	})
}

func TestS3SinglePutContract(t *testing.T) {
	porttest.RunBlobStore(t, func(t *testing.T) (ports.BlobStore, porttest.Corrupter) {
		f := testStore(t)
		f.native.Store(-1)
		return f.Store, f
	})
}

func TestS3Readiness(t *testing.T) {
	f := testStore(t)
	if err := f.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	caps := f.Capabilities()
	if !caps.Multipart || !caps.Presign || caps.Versioning {
		t.Fatalf("unversioned bucket capabilities: %+v", caps)
	}
	if err := f.client.EnableVersioning(context.Background(), f.bucket); err != nil {
		t.Fatal(err)
	}
	if err := f.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !f.Capabilities().Versioning {
		t.Fatal("enabled versioning was not detected")
	}
}

func TestS3NativeMultipart(t *testing.T) {
	f := testStore(t)
	ctx := context.Background()
	key := "uploads/session/tmp"
	id, err := f.StartMultipart(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.AbortMultipart(ctx, key, id); err != nil {
			t.Error(err)
		}
	})
	u, err := f.PartURL(ctx, key, id, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("PUT", u, strings.NewReader("resumable"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("presigned part: %d", resp.StatusCode)
	}
	parts, err := f.ListParts(ctx, key, id)
	if err != nil || len(parts) != 1 || parts[0].Size != 9 || parts[0].Number != 1 {
		t.Fatalf("resume: %+v %v", parts, err)
	}
	if err := f.FinishMultipart(ctx, key, id, parts); err != nil {
		t.Fatal(err)
	}
	r, err := f.Get(ctx, key, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil || string(b) != "resumable" {
		t.Fatalf("completed bytes: %q %v", b, err)
	}
}

func TestAbortUnacknowledgedMultipartUsesExactKey(t *testing.T) {
	f := testStore(t)
	ctx := context.Background()
	key := "uploads/one/assembled"
	keys := []string{key, key, key + "-sibling"}
	var ids []string
	for _, k := range keys {
		id, err := f.StartMultipart(ctx, k)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		t.Cleanup(func() { _ = f.AbortMultipart(ctx, k, id) })
	}
	if err := f.AbortMultipart(ctx, key, ""); err != nil {
		t.Fatal(err)
	}
	for i, k := range keys {
		_, err := f.ListParts(ctx, k, ids[i])
		if (k == key && !errors.Is(err, ports.ErrNotFound)) || (k != key && err != nil) {
			t.Fatalf("unexpected allocation state at %q: %v", k, err)
		}
	}
}

func TestPrivateDirectoryReclaimsAllVersions(t *testing.T) {
	f := testStore(t)
	ctx := context.Background()
	if err := f.client.EnableVersioning(ctx, f.bucket); err != nil {
		t.Fatal(err)
	}
	dir, sibling := "uploads/a", "uploads/ab"
	t.Cleanup(func() {
		for o := range f.client.Client.ListObjectsIter(ctx, f.bucket, minio.ListObjectsOptions{Recursive: true, WithVersions: true}) {
			if o.Err != nil {
				t.Error(o.Err)
				return
			}
			if err := f.DeleteVersion(ctx, o.Key, o.VersionID); err != nil {
				t.Error(err)
			}
		}
	})
	for _, key := range []string{dir + "/part", dir + "/part", dir + "/current", sibling + "/part"} {
		f.Corrupt(key, []byte("private data"))
	}
	if err := f.Delete(ctx, dir+"/part"); err != nil {
		t.Fatal(err)
	}
	if err := f.DeleteDirectoryVersions(ctx, dir); err != nil {
		t.Fatal(err)
	}
	var survivors int
	for o := range f.client.Client.ListObjectsIter(ctx, f.bucket, minio.ListObjectsOptions{Recursive: true, WithVersions: true}) {
		if o.Err != nil || o.Key != sibling+"/part" || o.IsDeleteMarker {
			t.Fatalf("unexpected remaining version at %q: %v", o.Key, o.Err)
		}
		survivors++
	}
	if survivors != 1 {
		t.Fatalf("expected the sibling version, got %d", survivors)
	}
}
