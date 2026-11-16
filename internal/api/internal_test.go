package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/core"
	"github.com/LUOSENGWA/MFGraph/internal/pathx"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

type seqIDs struct{ n int }

func (s *seqIDs) NewID() string { s.n++; return fmt.Sprintf("01JD3X5Q8Z9W2V4T6R8P0N2M%02d", s.n) }

func TestRecoverWritesInternalEnvelope(t *testing.T) {
	l := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := RequestID(&seqIDs{})(Recover(l)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != 500 || body["code"] != CodeInternal {
		t.Fatalf("panic response: %d %s", rec.Code, rec.Body)
	}
	if body["request_id"] != rec.Header().Get(HeaderRequestID) {
		t.Fatalf("request id mismatch: %v", body)
	}
}

func TestErrorMapping(t *testing.T) {
	_, perr := pathx.Normalize("../x")
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{NewError(CodeConflict, "generation mismatch", map[string]any{"current_generation": 7}), 409, CodeConflict},
		{NewError(CodeIdempotencyKeyReuse, "x", nil), 409, CodeIdempotencyKeyReuse},
		{NewError(CodeBlobMissing, "x", nil), 410, CodeBlobMissing},
		{NewError(CodeNotImplemented, "x", nil), 501, CodeNotImplemented},
		{fmt.Errorf("wrapped: %w", perr), 422, CodeInvalidPath},
		{fmt.Errorf("lookup: %w", ports.ErrNotFound), 404, CodeNotFound},
		{ports.ErrForbidden, 403, CodeForbidden},
		{ports.ErrUnauthenticated, 401, CodeUnauthenticated},
		{errors.New("secret detail: dsn=user:pw@db"), 500, CodeInternal},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		WriteError(rec, httptest.NewRequest("GET", "/", nil), c.err)
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != c.status || body["code"] != c.code {
			t.Errorf("%v: got %d %v", c.err, rec.Code, body)
		}
		if c.code == CodeInternal && body["message"] != "internal error" {
			t.Errorf("internal error leaks details: %v", body["message"])
		}
		if c.code == CodeInvalidPath && body["details"].(map[string]any)["kind"] != "dot_segment" {
			t.Errorf("invalid_path without kind: %v", body)
		}
	}
	for code, status := range codeStatus {
		if StatusOf(code) != status {
			t.Errorf("StatusOf(%s)", code)
		}
	}
	if StatusOf("unknown") != 500 {
		t.Error("unknown code must map to 500")
	}
}

func TestBearerToken(t *testing.T) {
	for h, want := range map[string]string{"Bearer abc": "abc", "bearer abc": "abc", "BEARER  abc ": "abc"} {
		if got, ok := bearerToken(h); !ok || got != want {
			t.Errorf("bearerToken(%q) = %q %v", h, got, ok)
		}
	}
	for _, h := range []string{"", "Bearer", "Bearer ", "Basic abc", "Bearer a b", "Bearerabc"} {
		if _, ok := bearerToken(h); ok {
			t.Errorf("bearerToken(%q) accepted", h)
		}
	}
	for _, ref := range []string{"", "a\nb", "a\xffb", strings.Repeat("x", 256)} {
		if _, err := NewStaticTokens(map[string]string{"tok": ref}); err == nil {
			t.Errorf("NewStaticTokens accepted the invalid actor reference %q", ref)
		}
	}
	if _, err := NewStaticTokens(map[string]string{"": "alice"}); err == nil {
		t.Error("NewStaticTokens accepted an empty token")
	}
	// Any valid actor reference is accepted; a ULID is also the principal UID.
	s, err := NewStaticTokens(map[string]string{"tok-a": "alice", "tok-b": "01JD3X5Q8Z9W2V4T6R8P0N2M4K"})
	if err != nil {
		t.Fatal(err)
	}
	for tok, want := range map[string]ports.Principal{
		"tok-a": {Ref: "alice"},
		"tok-b": {Ref: "01JD3X5Q8Z9W2V4T6R8P0N2M4K", PrincipalUID: "01JD3X5Q8Z9W2V4T6R8P0N2M4K"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		if p, err := s.Authenticate(r); err != nil || p != want {
			t.Errorf("token %s: %+v, %v; want %+v", tok, p, err, want)
		}
	}
}

func TestCoreErrorMapping(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{&core.Error{Code: core.CodeConflict, Message: "x", Details: map[string]any{"current_generation": 3}}, 409, CodeConflict},
		{&core.Error{Code: core.CodeBlobMissing, Message: "x"}, 410, CodeBlobMissing},
		{&core.Error{Code: core.CodeFileTooLarge, Message: "x"}, 413, CodeFileTooLarge},
		{&core.Error{Code: core.CodeDBUnavailable, Message: "x"}, 503, CodeDBUnavailable},
		{fmt.Errorf("wrapped: %w", &core.Error{Code: core.CodeNotFound, Message: "x"}), 404, CodeNotFound},
		{&core.Error{Code: "no_such_code", Message: "secret"}, 500, CodeInternal},
		{fmt.Errorf("db: %w", ports.ErrDBUnavailable), 503, CodeDBUnavailable},
	} {
		rec := httptest.NewRecorder()
		WriteError(rec, httptest.NewRequest("GET", "/", nil), c.err)
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != c.status || body["code"] != c.code {
			t.Errorf("%v: got %d %v", c.err, rec.Code, body)
		}
	}
}

func TestParseRange(t *testing.T) {
	for _, c := range []struct {
		h               string
		size            int64
		start, end      int64
		ok, satisfiable bool
	}{
		{"bytes=1-3", 6, 1, 3, true, true},
		{"bytes=0-", 6, 0, 5, true, true},
		{"bytes=4-100", 6, 4, 5, true, true},
		{"bytes=-2", 6, 4, 5, true, true},
		{"bytes=-100", 6, 0, 5, true, true},
		{"bytes=6-", 6, 0, 0, true, false},
		{"bytes=-0", 6, 0, 0, true, false},
		{"bytes=0-0", 0, 0, 0, true, false},
		{"bytes=-1", 0, 0, 0, true, false},
		{" bytes= 2 - 3 ", 6, 2, 3, true, true},
		{"bytes=3-1", 6, 0, 0, false, false},
		{"bytes=0-1,3-4", 6, 0, 0, false, false},
		{"items=0-1", 6, 0, 0, false, false},
		{"bytes=a-b", 6, 0, 0, false, false},
		{"bytes=+1-2", 6, 0, 0, false, false},
		{"bytes=", 6, 0, 0, false, false},
	} {
		s, e, ok, sat := parseRange(c.h, c.size)
		if ok != c.ok || sat != c.satisfiable || ok && sat && (s != c.start || e != c.end) {
			t.Errorf("parseRange(%q, %d) = %d %d %v %v", c.h, c.size, s, e, ok, sat)
		}
	}
	etag := `"sha256:abc"`
	for h, want := range map[string]bool{etag: true, "*": true, `W/"sha256:abc"`: true, `"x", ` + etag: true, `"sha256:abd"`: false, "": false} {
		if etagMatches(h, etag) != want {
			t.Errorf("etagMatches(%q) != %v", h, want)
		}
	}
}
