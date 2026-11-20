//go:build e2e

package e2e

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

func TestPackedContent(t *testing.T)             { testPackedContent(t, false) }
func TestStandaloneS3PackedContent(t *testing.T) { testPackedContent(t, true) }

func testPackedContent(t *testing.T, remote bool) {
	e := newEnv(t)
	if remote {
		useS3(t, e)
	}
	s := start(t, e)
	c := s.client(tokenA)
	sd := seed(t, c)
	s.stop()
	e["MFGRAPH_CHUNK_BYTES"], e["MFGRAPH_PACK_BYTES"] = "4194304", "67108864"
	s = start(t, e)
	c = s.client(tokenA)
	const n = 4 << 20
	data := strings.Repeat("a", n) + strings.Repeat("b", n) + strings.Repeat("a", n)
	h := c.upload(sd.ws.WorkspaceId, data)
	commit := c.commit(sd.ws.WorkspaceId, sd.branch.BranchId, sd.first.Generation, put("packed", h))
	db, err := sql.Open("mysql", e["MFGRAPH_DB_DSN"])
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var packs, chunks, refs, bytes int
	if err = db.QueryRow(`SELECT (SELECT COUNT(*) FROM packs),(SELECT COUNT(*) FROM chunks),(SELECT COUNT(*) FROM blob_chunks),
		(SELECT SUM(size_bytes) FROM packs)`).Scan(&packs, &chunks, &refs, &bytes); err != nil || packs != 1 || chunks != 2 || refs != 3 || bytes != 2*n {
		t.Fatalf("expected two unique physical chunks for three logical chunks: %d %d %d %d %v", packs, chunks, refs, bytes, err)
	}
	smallA, smallB := c.upload(sd.ws.WorkspaceId, "small-A"), c.upload(sd.ws.WorkspaceId, "small-B")
	next := c.commit(sd.ws.WorkspaceId, sd.branch.BranchId, commit.Generation, put("a-small", smallA), put("b-small", smallB))
	var shared int
	if err := db.QueryRow(`SELECT COUNT(DISTINCT c.pack_id) FROM blob_chunks b JOIN chunks c ON b.workspace_id=c.workspace_id AND b.chunk_hash=c.chunk_hash
        WHERE b.workspace_id=? AND b.blob_hash IN (?,?)`, sd.ws.WorkspaceId, strings.TrimPrefix(smallA, "sha256:"), strings.TrimPrefix(smallB, "sha256:")).Scan(&shared); err != nil || shared != 1 {
		t.Fatalf("small files do not share a pack: %d %v", shared, err)
	}
	s.stop()
	delete(e, "MFGRAPH_CHUNK_BYTES")
	delete(e, "MFGRAPH_PACK_BYTES")
	c = start(t, e).client(tokenA)
	if r := c.file(next.SnapshotId, "b-small", map[string]string{"Range": "bytes=4-6"}); r.status != 206 || string(r.body) != "l-B" {
		t.Fatalf("small file in shared pack: %d %q", r.status, r.body)
	}

	if r := c.file(sd.first.SnapshotId, "a.txt", nil); r.status != 200 || string(r.body) != contentA {
		t.Fatal("old whole-file snapshot unreadable")
	}
	if r := c.file(commit.SnapshotId, "packed", map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", n-2, n+1)}); r.status != 206 || string(r.body) != "aabb" || r.header.Get("ETag") != `"`+h+`"` {
		t.Fatalf("range across two chunks: %d %q", r.status, r.body)
	}
	if r := c.file(commit.SnapshotId, "packed", nil); r.status != 200 || sha(string(r.body)) != h {
		t.Fatal("whole-file SHA-256 changed")
	}
	if got := c.upload(sd.ws.WorkspaceId, data); got != h {
		t.Fatal("whole-file reupload changed identity")
	}
}
