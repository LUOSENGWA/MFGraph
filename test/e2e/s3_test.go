//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func useS3(t *testing.T, e env) *minio.Client {
	t.Helper()
	endpoint := os.Getenv("MFGRAPH_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set MFGRAPH_TEST_S3_ENDPOINT to test a real bucket")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	e["MFGRAPH_BLOB_BACKEND"], e["MFGRAPH_S3_ENDPOINT"], e["AWS_REGION"] = "s3", endpoint, "us-east-1"
	e["AWS_ACCESS_KEY_ID"] = os.Getenv("MFGRAPH_TEST_S3_ACCESS_KEY")
	e["AWS_SECRET_ACCESS_KEY"] = os.Getenv("MFGRAPH_TEST_S3_SECRET_KEY")
	bucket := "mfgraph-e2e-" + strings.ToLower(rand.Text())
	e["MFGRAPH_S3_BUCKET"] = bucket
	c, err := minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4(e["AWS_ACCESS_KEY_ID"], e["AWS_SECRET_ACCESS_KEY"], ""), Secure: u.Scheme == "https", Region: e["AWS_REGION"]})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: e["AWS_REGION"]}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for o := range c.ListObjects(ctx, bucket, minio.ListObjectsOptions{Recursive: true}) {
			if o.Err != nil {
				t.Error(o.Err)
				break
			}
			if err := c.RemoveObject(ctx, bucket, o.Key, minio.RemoveObjectOptions{}); err != nil {
				t.Error(err)
			}
		}
		if err := c.RemoveBucket(ctx, bucket); err != nil {
			t.Error(err)
		}
	})
	return c
}

func TestStandaloneS3(t *testing.T) {
	e := newEnv(t)
	// Seed a local workspace first and keep its files through the switch.
	local := start(t, e)
	old := seed(t, local.client(tokenA))
	local.stop()
	useS3(t, e)
	s := start(t, e)
	c := s.client(tokenA)
	c.expect(c.json("GET", "/readyz", nil, nil), "S3 ready", 200, nil)
	c.expect(c.file(old.first.SnapshotId, "a.txt", nil), "old backend unavailable", 503, nil)
	sd := seed(t, c)
	s.stop()
	c = start(t, e).client(tokenA)
	r := c.file(sd.first.SnapshotId, "a.txt", map[string]string{"Range": "bytes=1-3"})
	if r.status != 206 || string(r.body) != contentA[1:4] {
		t.Fatalf("S3 after restart: %d %q", r.status, r.body)
	}
}
