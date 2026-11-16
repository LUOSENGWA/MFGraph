package api_test

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/core"
)

// TestDownloadHeaders: downloads are attachments with the file name
// (RFC 6266 filename* and an ASCII fallback) and nosniff, whole or ranged.
func TestDownloadHeaders(t *testing.T) {
	srv := newServiceServer(t, "", core.Config{})
	c := caller{t: t, srv: srv, prefix: "/api/v1", token: token}
	var proj struct {
		ProjectID string `json:"project_id"`
	}
	c.json("POST", "/projects", `{"name":"downloads"}`, 201, &proj)
	var ws struct {
		ID   string `json:"workspace_id"`
		Main string `json:"main_branch_id"`
	}
	c.json("POST", "/workspaces", `{"project_id":"`+proj.ProjectID+`"}`, 201, &ws)
	resp, _ := c.call("PUT", "/workspaces/"+ws.ID+"/objects", "content", map[string]string{"Content-Type": "application/octet-stream"})
	if resp.StatusCode != 201 {
		t.Fatalf("upload: %d", resp.StatusCode)
	}
	cases := []struct{ path, want string }{
		{"a.txt", `attachment; filename="a.txt"; filename*=UTF-8''a.txt`},
		{"dir/a b.txt", `attachment; filename="a b.txt"; filename*=UTF-8''a%20b.txt`},
		{"\u6587\u4ef6.txt", `attachment; filename="__.txt"; filename*=UTF-8''%E6%96%87%E4%BB%B6.txt`},
		{"q\"x%;z.txt", `attachment; filename="q_x_;z.txt"; filename*=UTF-8''q%22x%25%3Bz.txt`},
	}
	changes := ""
	for i, tc := range cases {
		if i > 0 {
			changes += ","
		}
		b, _ := jsonString(tc.path)
		changes += `{"path":` + b + `,"hash":"` + shaOf("content") + `"}`
	}
	var snap struct {
		ID string `json:"snapshot_id"`
	}
	c.json("POST", "/workspaces/"+ws.ID+"/commits", `{"branch_id":"`+ws.Main+`","expected_generation":1,"message":"m","changes":[`+changes+`]}`,
		201, &snap)
	for _, tc := range cases {
		for _, rng := range []string{"", "bytes=1-2"} {
			hdr := map[string]string{}
			if rng != "" {
				hdr["Range"] = rng
			}
			resp, body := c.call("GET", "/snapshots/"+snap.ID+"/file?path="+url.QueryEscape(tc.path), "", hdr)
			if want := map[string]int{"": 200, "bytes=1-2": 206}[rng]; resp.StatusCode != want {
				t.Fatalf("%q %q: %d %s", tc.path, rng, resp.StatusCode, body)
			}
			if got := resp.Header.Get("Content-Disposition"); got != tc.want {
				t.Errorf("%q %q: Content-Disposition %s, want %s", tc.path, rng, got, tc.want)
			}
			if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Content-Type") != "application/octet-stream" {
				t.Errorf("%q %q: headers %v", tc.path, rng, resp.Header)
			}
		}
	}
}

func jsonString(s string) (string, error) {
	b, err := json.Marshal(s)
	return string(b), err
}
