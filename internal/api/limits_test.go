package api_test

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/api"
	"github.com/LUOSENGWA/MFGraph/internal/api/gen"
	"github.com/LUOSENGWA/MFGraph/internal/core"
)

func TestSlowUploadDeadlineAndCapacity(t *testing.T) {
	const prefix = "/api/v1"
	srv := httptest.NewServer(newServiceHandler(t, prefix, core.Config{}, func(o *api.Options) { o.UploadConcurrency = 1; o.UploadTimeout = time.Second }))
	defer srv.Close()
	c := caller{t: t, srv: srv, prefix: prefix, token: token}
	var project gen.Project
	c.json("POST", "/projects", `{"name":"p"}`, 201, &project)
	var ws gen.Workspace
	c.json("POST", "/workspaces", `{"project_id":"`+project.ProjectId+`"}`, 201, &ws)
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(srv.URL, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	path := "/objects/" + ws.WorkspaceId
	// The first byte is sent; the remainder never arrives.
	_, err = fmt.Fprintf(conn, "PUT %s%s HTTP/1.1\r\nHost: local\r\nAuthorization: Bearer %s\r\nContent-Length: 8\r\nExpect: 100-continue\r\n\r\n", prefix, path, token)
	if err != nil {
		t.Fatal(err)
	}
	wire := bufio.NewReader(conn)
	interim, err := http.ReadResponse(wire, nil)
	if err != nil || interim.StatusCode != 100 {
		t.Fatalf("body was not accepted: %v %v", interim, err)
	}
	interim.Body.Close()
	if _, err := fmt.Fprint(conn, "x"); err != nil {
		t.Fatal(err)
	}
	limited, _ := c.call("PUT", path, "data", nil)
	if limited.StatusCode != 429 || limited.Header.Get("Retry-After") == "" {
		t.Fatalf("slow upload did not hold its slot: %d", limited.StatusCode)
	}
	resp, err := http.ReadResponse(wire, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 408 {
		t.Fatalf("timed out body returned %d", resp.StatusCode)
	}
	after, _ := c.call("PUT", path, "after", nil)
	if after.StatusCode != 201 {
		t.Fatalf("upload slot was not released: %d", after.StatusCode)
	}
}
