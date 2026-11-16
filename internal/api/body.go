package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
)

// DefaultMaxJSONBodyBytes is the default limit of a request body other than
// an upload: 16 MiB.
const DefaultMaxJSONBodyBytes = 16 << 20

// ReasonUnsupportedContentType is details.reason of a JSON body sent with
// another Content-Type (415, code invalid_request).
const ReasonUnsupportedContentType = "unsupported_content_type"

// uploadPattern is the route of uploads, which stream their body and have a
// limit of their own (file_too_large).
const uploadPattern = http.MethodPut + " " + apiBase + "/workspaces/{workspace_id}/objects"

// requestBody checks the body of every request of a route other than the
// upload route, before the generated handler decodes it:
//
//   - a body above limit bytes gives 413 request_too_large with
//     details.max_bytes: at once when Content-Length says so, and
//     otherwise as soon as more than limit bytes have arrived
//     (http.MaxBytesReader), so it is never read beyond the limit; the
//     connection is closed after the response instead of being drained;
//   - a POST (every POST takes a JSON body) must declare Content-Type
//     application/json, with no parameter but charset=utf-8; anything else
//     gives 415 with code invalid_request and details.reason
//     unsupported_content_type;
//   - a POST body that starts with a JSON value must end with it (white
//     space aside): data after it gives 400 invalid_request (the generated
//     decoder would ignore it). A body that is not JSON at all is left to
//     the generated handler, which reports it as invalid_request.
//
// A POST body is read into memory (at most limit bytes) and handed on.
func requestBody(limit int64) func(http.Handler) http.Handler {
	if limit <= 0 {
		limit = DefaultMaxJSONBodyBytes
	}
	tooLarge := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		WriteError(w, r, NewError(CodeRequestTooLarge, fmt.Sprintf("the request body exceeds the limit of %d bytes", limit),
			map[string]any{"max_bytes": limit}))
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Pattern == uploadPattern {
				next.ServeHTTP(w, r)
				return
			}
			if r.ContentLength > limit {
				tooLarge(w, r)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			if r.Method != http.MethodPost {
				next.ServeHTTP(w, r)
				return
			}
			if !isJSONMediaType(r.Header.Get("Content-Type")) {
				e := NewError(CodeInvalidRequest, "the request body must be sent with Content-Type: application/json",
					map[string]any{"reason": ReasonUnsupportedContentType})
				e.Status = http.StatusUnsupportedMediaType
				WriteError(w, r, e)
				return
			}
			body, err := io.ReadAll(r.Body)
			var mbe *http.MaxBytesError
			switch {
			case errors.As(err, &mbe):
				tooLarge(w, r)
				return
			case err != nil:
				WriteError(w, r, NewError(CodeInvalidRequest, "the request body could not be read", nil))
				return
			}
			if hasTrailingData(body) {
				WriteError(w, r, NewError(CodeInvalidRequest, "the request body has data after the JSON value", nil))
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			next.ServeHTTP(w, r)
		})
	}
}

// isJSONMediaType accepts application/json, optionally with charset=utf-8
// (JSON is always UTF-8, RFC 8259).
func isJSONMediaType(ct string) bool {
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil || mt != "application/json" {
		return false
	}
	for k, v := range params {
		if k != "charset" || !strings.EqualFold(v, "utf-8") {
			return false
		}
	}
	return true
}

// hasTrailingData reports a body that starts with a complete JSON value and
// has more than white space after it.
func hasTrailingData(body []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(body))
	var v json.RawMessage
	if err := dec.Decode(&v); err != nil {
		return false
	}
	_, err := dec.Token()
	return err != io.EOF
}
