package s3

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"strings"
	"testing"

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

func TestS3Readiness(t *testing.T) {
	f := testStore(t)
	if err := f.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
}
