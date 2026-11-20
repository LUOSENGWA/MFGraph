package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTreeCapacityLeavesHealthAndUploadsAvailable(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	h := limitWork(Options{TreeConcurrency: 1, UploadConcurrency: 1})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hold" {
			close(entered)
			<-release
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	call := func(path, pattern string) int {
		r := httptest.NewRequest("GET", path, nil)
		r.Pattern = pattern
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	done := make(chan struct{})
	go func() { defer close(done); call("/hold", "GET "+apiBase+"/snapshots/{snapshot_id}/tree") }()
	<-entered
	tree := call("/other", "POST "+apiBase+"/workspaces/{workspace_id}/commits")
	upload := call("/upload", "PUT "+apiBase+"/objects/{workspace_id}")
	ready := call("/readyz", "GET /readyz")
	close(release)
	<-done
	if tree != 429 || upload != 204 || ready != 204 {
		t.Fatalf("concurrent tree=%d upload=%d ready=%d", tree, upload, ready)
	}
	if got := call("/again", "GET "+apiBase+"/snapshots/{snapshot_id}/diff"); got != 204 {
		t.Fatalf("slot was not released: %d", got)
	}
}
