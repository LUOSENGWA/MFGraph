//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
)

func TestStandaloneS3CrashOpeningUpload(t *testing.T) {
	e := newEnv(t)
	backend := minio.Core{Client: useS3(t, e)}
	target, err := url.Parse(e["MFGRAPH_S3_ENDPOINT"])
	if err != nil {
		t.Fatal(err)
	}
	type allocation struct{ Key, UploadId string }
	created := make(chan allocation, 1)
	release := make(chan struct{})
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ModifyResponse = func(resp *http.Response) error {
		r := resp.Request
		if r.Method != http.MethodPost || !r.URL.Query().Has("uploads") || !strings.HasSuffix(r.URL.Path, "/assembled") || resp.StatusCode != http.StatusOK {
			return nil
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}
		var a allocation
		if err := xml.Unmarshal(body, &a); err != nil {
			return err
		}
		created <- a
		select {
		case <-r.Context().Done():
		case <-release:
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}
	gateway := httptest.NewServer(proxy)
	defer gateway.Close()
	defer close(release)
	e["MFGRAPH_S3_ENDPOINT"] = gateway.URL
	s := start(t, e)
	sd := seed(t, s.client(tokenA))
	request, err := json.Marshal(map[string]any{"workspace_id": sd.ws.WorkspaceId, "path": "interrupted", "size_bytes": 7})
	if err != nil {
		t.Fatal(err)
	}
	done := duringRequest(t, s, "POST", "/api/v1/uploads", bytes.NewReader(request))
	var a allocation
	select {
	case a = <-created:
	case <-time.After(10 * time.Second):
		t.Fatal("S3 never acknowledged creating the upload")
	}
	ctx := context.Background()
	bucket := e["MFGRAPH_S3_BUCKET"]
	defer backend.AbortMultipartUpload(ctx, bucket, a.Key, a.UploadId)
	if _, err := backend.ListObjectParts(ctx, bucket, a.Key, a.UploadId, 0, 1); err != nil {
		t.Fatal(err)
	}
	killServer(t, s)
	<-done
	db, err := sql.Open("mysql", e["MFGRAPH_DB_DSN"])
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var id, status, nativeID string
	var reserved int64
	if err := db.QueryRow(`SELECT upload_id,status,backend_upload_id,reserved_bytes FROM upload_sessions
		JOIN workspace_usage USING(workspace_id) WHERE workspace_id=?`, sd.ws.WorkspaceId).Scan(&id, &status, &nativeID, &reserved); err != nil ||
		status != "opening" || nativeID != "" || reserved != 7 {
		t.Fatalf("unacknowledged allocation: status=%q native=%q reserved=%d err=%v", status, nativeID, reserved, err)
	}
	if _, err := db.Exec("UPDATE upload_sessions SET expires_at_us=0 WHERE upload_id=?", id); err != nil {
		t.Fatal(err)
	}
	start(t, e)
	for deadline := time.Now().Add(6 * time.Second); ; time.Sleep(30 * time.Millisecond) {
		var cleaned sql.NullInt64
		err := db.QueryRow(`SELECT status,cleaned_at_us,reserved_bytes FROM upload_sessions
			JOIN workspace_usage USING(workspace_id) WHERE upload_id=?`, id).Scan(&status, &cleaned, &reserved)
		if err == nil && status == "expired" && cleaned.Valid && reserved == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("abandoned allocation: status=%q cleaned=%v reserved=%d err=%v", status, cleaned, reserved, err)
		}
	}
	if _, err := backend.ListObjectParts(ctx, bucket, a.Key, a.UploadId, 0, 1); minio.ToErrorResponse(err).Code != "NoSuchUpload" {
		t.Fatalf("the native allocation survived cleanup: %v", err)
	}
}
