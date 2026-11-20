//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/api/gen"
)

// The process is killed from outside. The tests wait for an observable IO
// boundary or hold the real branch row, without test hooks in the binary.
func killServer(t *testing.T, s *server) {
	t.Helper()
	if err := s.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-s.done
	s.stopped = true
}

func duringRequest(t *testing.T, s *server, method, path string, body io.Reader) <-chan struct{} {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, method, s.base+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tokenA)
	req.Header.Set("Content-Type", "application/json")
	if method == "PUT" {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	return done
}

func countFiles(t *testing.T, root, suffix string) int {
	t.Helper()
	var count int
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, suffix) {
			count++
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func waitForFiles(t *testing.T, root, suffix string, previous int, done <-chan struct{}) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if countFiles(t, root, suffix) > previous {
			return
		}
		select {
		case <-done:
			t.Fatalf("request ended before a new %s appeared", suffix)
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for a new %s", suffix)
}

func TestCrashDuringUploadAndCommit(t *testing.T) {
	e := newEnv(t)
	s := start(t, e)
	c := s.client(tokenA)
	sd := seed(t, c)
	ws, br, base := sd.ws.WorkspaceId, sd.branch.BranchId, sd.first
	root := e["MFGRAPH_STORAGE_ROOT"]
	db, err := sql.Open("mysql", e["MFGRAPH_DB_DSN"])
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pending := countFiles(t, root, ".part")
	pr, pw := io.Pipe()
	defer pw.Close()
	done := duringRequest(t, s, "PUT", "/api/v1/objects/"+ws, pr)
	if _, err = pw.Write([]byte("partial upload")); err != nil {
		t.Fatal(err)
	}
	waitForFiles(t, root, ".part", pending, done)
	killServer(t, s)
	pw.Close()
	<-done
	var records int
	if err := db.QueryRow("SELECT COUNT(*) FROM blobs WHERE workspace_id=? AND blob_hash=?", ws, strings.TrimPrefix(sha("partial upload"), "sha256:")).Scan(&records); err != nil || records != 0 {
		t.Fatalf("partial blob: %d %v", records, err)
	}

	s = start(t, e)
	c = s.client(tokenA)
	hash := c.upload(ws, "complete upload")
	req := gen.CommitRequest{BranchId: br, ExpectedGeneration: base.Generation, Changes: []gen.Change{put("new.txt", hash)}}
	wire, _ := json.Marshal(req)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := tx.QueryRow("SELECT generation FROM branches WHERE branch_id=? FOR UPDATE", br).Scan(&records); err != nil {
		t.Fatal(err)
	}
	nodes := countFiles(t, root, ".json.zst")
	done = duringRequest(t, s, "POST", "/api/v1/workspaces/"+ws+"/commits", bytes.NewReader(wire))
	waitForFiles(t, root, ".json.zst", nodes, done)
	killServer(t, s)
	<-done
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	s = start(t, e)
	c = s.client(tokenA)
	if b := c.mainBranch(sd.ws); b.Generation != base.Generation || *b.HeadSnapshotId != base.SnapshotId {
		t.Fatalf("partially visible commit: %+v", b)
	}
	var snapCount, evCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM snapshots WHERE workspace_id=?", ws).Scan(&snapCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM outbox_events WHERE workspace_id=?", ws).Scan(&evCount); err != nil || snapCount != 1 || evCount != 4 {
		t.Fatalf("snapshot/outbox after crash: %d/%d %v", snapCount, evCount, err)
	}
	var committed gen.CommitResult
	c.expect(c.commitRaw(ws, req, "crash-retry"), "retry", 201, &committed)
	killServer(t, s)
	c = start(t, e).client(tokenA)
	var replay gen.CommitResult
	c.expect(c.commitRaw(ws, req, "crash-retry"), "retry after durable commit", 201, &replay)
	if replay.SnapshotId != committed.SnapshotId || string(c.file(replay.SnapshotId, "new.txt", nil).body) != "complete upload" {
		t.Fatal("durable commit or content changed after restart")
	}
}
