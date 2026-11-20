package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/api"
	"github.com/LUOSENGWA/MFGraph/internal/core"
)

// endless is a body that never ends and counts the bytes read from it.
type endless struct{ n int64 }

func (e *endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	e.n += int64(len(p))
	return len(p), nil
}

func serve(h http.Handler, method, path string, body io.Reader, size int64, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	if size >= 0 {
		req.ContentLength = size
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func errorOf(t *testing.T, rec *httptest.ResponseRecorder) envelope {
	t.Helper()
	var e envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("%d %q: %v", rec.Code, rec.Body, err)
	}
	return e
}

// TestRequestBodyLimit: a body above MaxJSONBodyBytes gives 413
// request_too_large, and is never read beyond the limit.
func TestRequestBodyLimit(t *testing.T) {
	const limit = 1024
	h := newServiceHandler(t, "", core.Config{MaxUploadBytes: 1 << 20}, func(o *api.Options) { o.MaxJSONBodyBytes = limit })
	jsonHdr := map[string]string{"Content-Type": "application/json"}

	// Content-Length above the limit: refused before reading.
	body := &endless{}
	rec := serve(h, "POST", "/api/v1/projects", body, 1<<40, jsonHdr)
	e := errorOf(t, rec)
	if rec.Code != 413 || e.Code != api.CodeRequestTooLarge || e.Details["max_bytes"] != float64(limit) || body.n != 0 {
		t.Fatalf("declared size over the limit: %d %+v, %d bytes read", rec.Code, e, body.n)
	}
	if rec.Header().Get("Connection") != "close" {
		t.Errorf("413 without Connection: close")
	}
	// Unknown length (chunked): refused once more than limit bytes came.
	body = &endless{}
	rec = serve(h, "POST", "/api/v1/projects", body, -1, jsonHdr)
	if e := errorOf(t, rec); rec.Code != 413 || e.Code != api.CodeRequestTooLarge || body.n > limit+1 {
		t.Fatalf("endless body: %d %+v, %d bytes read (limit %d)", rec.Code, e, body.n, limit)
	}
	// Exactly the limit is accepted, one byte more is not.
	exact := `{"name":"demo"}` + strings.Repeat(" ", limit-len(`{"name":"demo"}`))
	if rec := serve(h, "POST", "/api/v1/projects", strings.NewReader(exact), -1, jsonHdr); rec.Code != 201 {
		t.Fatalf("a body of exactly the limit: %d %s", rec.Code, rec.Body)
	}
	if rec := serve(h, "POST", "/api/v1/projects", strings.NewReader(exact+" "), -1, jsonHdr); rec.Code != 413 {
		t.Fatalf("a body one byte over the limit: %d %s", rec.Code, rec.Body)
	}
	// Uploads have their own limit.
	up := strings.Repeat("u", 4*limit)
	rec = serve(h, "PUT", "/api/v1/workspaces/"+ulid1+"/objects", strings.NewReader(up), int64(len(up)),
		map[string]string{"Content-Type": "application/octet-stream"})
	if e := errorOf(t, rec); e.Code == api.CodeRequestTooLarge {
		t.Fatalf("the upload was limited by the JSON body limit: %d %+v", rec.Code, e)
	}
	// Other routes are limited too.
	body = &endless{}
	rec = serve(h, "GET", "/api/v1/projects/"+ulid1, body, 1<<40, nil)
	if rec.Code != 413 || body.n != 0 {
		t.Fatalf("GET with a declared body over the limit: %d, %d bytes read", rec.Code, body.n)
	}
}

// TestRequestBodyContentTypeAndTrailingData: JSON bodies need Content-Type
// application/json and must be a single JSON value.
func TestRequestBodyContentTypeAndTrailingData(t *testing.T) {
	h := newServiceHandler(t, "", core.Config{}, nil)
	post := func(ct, body string) *httptest.ResponseRecorder {
		hdr := map[string]string{}
		if ct != "" {
			hdr["Content-Type"] = ct
		}
		return serve(h, "POST", "/api/v1/projects", strings.NewReader(body), -1, hdr)
	}
	for _, ct := range []string{"application/json", "application/json; charset=utf-8", "Application/JSON;charset=UTF-8"} {
		if rec := post(ct, `{"name":"demo"}`); rec.Code != 201 {
			t.Errorf("Content-Type %q: %d %s", ct, rec.Code, rec.Body)
		}
	}
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "application/json; charset=latin1",
		"application/json; foo=bar", "application/jsonx", "json"} {
		rec := post(ct, `{"name":"demo"}`)
		if e := errorOf(t, rec); rec.Code != 415 || e.Code != api.CodeInvalidRequest || e.Details["reason"] != api.ReasonUnsupportedContentType {
			t.Errorf("Content-Type %q: %d %+v", ct, rec.Code, e)
		}
	}
	for _, body := range []string{`{"name":"a"}{"name":"b"}`, `{"name":"a"} x`, `{"name":"a"},`, `{"name":"a"}]`} {
		rec := post("application/json", body)
		if e := errorOf(t, rec); rec.Code != 400 || e.Code != api.CodeInvalidRequest || !strings.Contains(e.Message, "after the JSON value") {
			t.Errorf("body %q: %d %+v", body, rec.Code, e)
		}
	}
	if rec := post("application/json", "{\"name\":\"a\"}\n\t \r\n"); rec.Code != 201 {
		t.Errorf("white space after the value: %d %s", rec.Code, rec.Body)
	}
	for _, name := range []string{"caf\xe9", `\uD800`, `\uDC00`, `\uD800\u0041`} {
		rec := post("application/json", `{"name":"`+name+`"}`)
		if rec.Code != 400 || errorOf(t, rec).Code != api.CodeInvalidRequest {
			t.Errorf("invalid Unicode %q: %d %s", name, rec.Code, rec.Body)
		}
	}
	for _, name := range []string{"café", `\uD83D\uDE00`, `\\ud800`, `\uFFFD`} {
		if rec := post("application/json", `{"name":"`+name+`"}`); rec.Code != 201 {
			t.Errorf("valid Unicode %q: %d %s", name, rec.Code, rec.Body)
		}
	}
	// Bodies that are not JSON are refused by the generated decoder.
	for _, body := range []string{"", "{", "nope"} {
		if rec := post("application/json", body); rec.Code != 400 {
			t.Errorf("body %q: %d %s", body, rec.Code, rec.Body)
		}
	}
	// Unknown routes stay 404, whatever the body.
	if rec := serve(h, "POST", "/api/v1/nope", strings.NewReader("x"), -1, nil); rec.Code != 404 {
		t.Errorf("unknown route: %d", rec.Code)
	}
}
