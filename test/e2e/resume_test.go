//go:build e2e

package e2e

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/api/gen"
)

func TestResumableUpload(t *testing.T)            { testResumable(t, "posix", false) }
func TestStandaloneS3Resumable(t *testing.T)      { testResumable(t, "s3", false) }
func TestStandaloneS3RelayResumable(t *testing.T) { testResumable(t, "s3-relay", false) }
func TestLargeResumableUpload(t *testing.T) {
	if os.Getenv("MFGRAPH_E2E_LARGE_RESUME") != "1" {
		t.Skip("set MFGRAPH_E2E_LARGE_RESUME=1 to stream 1 GiB")
	}
	testResumable(t, "posix", true)
}

func testResumable(t *testing.T, backend string, large bool) {
	e := newEnv(t)
	if backend != "posix" {
		useS3(t, e)
	}
	if backend == "s3-relay" {
		target, err := url.Parse(e["MFGRAPH_S3_ENDPOINT"])
		if err != nil {
			t.Fatal(err)
		}
		upstream := httputil.NewSingleHostReverseProxy(target)
		gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Query().Has("uploads") {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusNotImplemented)
				io.WriteString(w, "<Error><Code>NotImplemented</Code></Error>")
				return
			}
			upstream.ServeHTTP(w, r)
		}))
		t.Cleanup(gateway.Close)
		e["MFGRAPH_S3_ENDPOINT"] = gateway.URL
	}
	e["MFGRAPH_CHUNK_BYTES"], e["MFGRAPH_PACK_BYTES"] = "4194304", "67108864"
	s := start(t, e)
	c := s.client(tokenA)
	sd := seed(t, c)
	ws := sd.ws.WorkspaceId
	size, partSize := int64(5<<20)+3, int64(5<<20)
	if large {
		size, partSize = 1<<30, 16<<20
	}
	contents := func(n int) string {
		return strings.Repeat(string(byte('A'+(n-1)%26)), int(min(partSize, size-int64(n-1)*partSize)))
	}
	total := int((size + partSize - 1) / partSize)
	h := sha256.New()
	for n := 1; n <= total; n++ {
		io.WriteString(h, contents(n))
	}
	expected := fmt.Sprintf("sha256:%x", h.Sum(nil))
	startBody := map[string]any{"workspace_id": ws, "path": "video.bin", "size_bytes": size, "part_size_bytes": partSize, "checksum_sha256": expected}
	var ticket gen.UploadTicket
	c.expect(c.json("POST", "/api/v1/uploads", startBody, map[string]string{"Idempotency-Key": "init"}), "init", 201, &ticket)
	if ticket.TotalParts != total || ticket.PartSizeBytes != partSize || len(ticket.Parts) == 0 {
		t.Fatalf("wrong negotiated sizes %+v", ticket)
	}
	path := "/api/v1/uploads/" + ticket.UploadId
	putPart := func(link gen.UploadPartURL) gen.PartETag {
		t.Helper()
		var tag string
		if backend == "s3" {
			if !strings.HasPrefix(link.Url, e["MFGRAPH_S3_ENDPOINT"]+"/") {
				t.Fatal("native upload did not issue a URL for this endpoint")
			}
			req, err := http.NewRequestWithContext(context.Background(), "PUT", link.Url, strings.NewReader(contents(link.PartNumber)))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("S3 part returned %d", resp.StatusCode)
			}
			tag = resp.Header.Get("ETag")
		} else {
			if !strings.HasPrefix(link.Url, path+"/parts?") {
				t.Fatalf("unexpected relay %q", link.Url)
			}
			r := c.do("PUT", link.Url, strings.NewReader(contents(link.PartNumber)), map[string]string{"Content-Type": "application/octet-stream"})
			c.expect(r, "relay part", 200, nil)
			tag = r.header.Get("ETag")
		}
		if tag == "" {
			t.Fatal("part has no ETag")
		}
		return gen.PartETag{PartNumber: link.PartNumber, Etag: tag}
	}
	parts := []gen.PartETag{putPart(ticket.Parts[0])}
	killServer(t, s)
	s = start(t, e)
	c = s.client(tokenA)
	var progress gen.UploadStatus
	c.expect(c.json("GET", path, nil, nil), "status after crash", 200, &progress)
	if len(progress.Uploaded) != 1 || progress.Uploaded[0].PartNumber != 1 || len(progress.Missing) != total-1 {
		t.Fatalf("resume status %+v", progress)
	}
	c.expect(s.client(tokenB).json("GET", path, nil, nil), "another identity", 404, nil)
	var links gen.UploadURLs
	c.expect(c.json("POST", path+"/parts", gen.PartNumbers{PartNumbers: progress.Missing}, nil), "fresh URLs", 200, &links)
	for _, link := range links.Parts {
		parts = append(parts, putPart(link))
	}
	complete := gen.CompleteUploadBody{Parts: parts}
	var receipt gen.UploadReceipt
	c.expect(c.json("POST", path+"/complete", complete, nil), "complete", 200, &receipt)
	if receipt.BlobHash != expected || !receipt.Staged || receipt.SizeBytes != size {
		t.Fatal("incorrect staged identity or size")
	}
	killServer(t, s)
	s = start(t, e)
	c = s.client(tokenA)
	var replay gen.UploadReceipt
	c.expect(c.json("POST", path+"/complete", complete, nil), "replay after second crash", 200, &replay)
	if replay != receipt {
		t.Fatal("completed receipt changed on replay")
	}
	db, err := sql.Open("mysql", e["MFGRAPH_DB_DSN"])
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var used, reserved int64
	var head string
	if err := db.QueryRow("SELECT used_bytes,reserved_bytes FROM workspace_usage WHERE workspace_id=?", ws).Scan(&used, &reserved); err != nil || used < size || reserved != 0 {
		t.Fatalf("ledger %d/%d %v", used, reserved, err)
	}
	if err := db.QueryRow("SELECT head_snapshot_id FROM branches WHERE branch_id=?", sd.branch.BranchId).Scan(&head); err != nil || head != sd.first.SnapshotId {
		t.Fatalf("upload moved the branch %q %v", head, err)
	}
	commit := c.commit(ws, sd.branch.BranchId, sd.first.Generation, put("video.bin", receipt.BlobHash))
	r := c.file(commit.SnapshotId, "video.bin", map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", partSize-1, partSize)})
	if r.status != 206 || string(r.body) != "AB" {
		t.Fatalf("range over resumed boundary: %d %q", r.status, r.body)
	}
}

func TestSessionExpiryAndAbort(t *testing.T) {
	e := newEnv(t)
	e["MFGRAPH_UPLOAD_SESSION_TTL"] = "3s"
	s := start(t, e)
	c := s.client(tokenA)
	sd := seed(t, c)
	init := func() string {
		var u gen.UploadTicket
		c.expect(c.json("POST", "/api/v1/uploads", map[string]any{"workspace_id": sd.ws.WorkspaceId, "path": "pending", "size_bytes": 3}, nil), "init", 201, &u)
		return "/api/v1/uploads/" + u.UploadId
	}
	path := init()
	c.expect(c.json("DELETE", path, nil, nil), "abort", 204, nil)
	c.expect(c.json("DELETE", path, nil, nil), "repeat abort", 204, nil)
	path = init()
	for deadline := time.Now().Add(8 * time.Second); ; {
		r := c.json("GET", path, nil, nil)
		if r.status == 410 && r.code() == "upload_expired" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never expired: %d %s", r.status, r.body)
		}
		time.Sleep(100 * time.Millisecond)
	}
	db, err := sql.Open("mysql", e["MFGRAPH_DB_DSN"])
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var reserved int64
	if err := db.QueryRow("SELECT reserved_bytes FROM workspace_usage WHERE workspace_id=?", sd.ws.WorkspaceId).Scan(&reserved); err != nil || reserved != 0 {
		t.Fatalf("after expiry: reserved=%d %v", reserved, err)
	}
}
