package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

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
	if _, err := NewStaticTokens(map[string]string{"tok": "not-a-ulid"}); err == nil {
		t.Error("NewStaticTokens accepted an invalid UID")
	}
}
