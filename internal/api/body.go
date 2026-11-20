package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// DefaultMaxJSONBodyBytes is the default limit of a request body other than
// an upload: 16 MiB.
const DefaultMaxJSONBodyBytes = 16 << 20

// ReasonUnsupportedContentType is details.reason of a JSON body sent with
// another Content-Type (415, code invalid_request).
const ReasonUnsupportedContentType = "unsupported_content_type"

// uploadPatterns are the routes of uploads, which stream their body and
// have a limit of their own (file_too_large): the upload of the first core
// slice and the same operation under the upload path /objects/.
var uploadPatterns = map[string]bool{
	http.MethodPut + " " + apiBase + "/uploads/{upload_id}/parts":         true,
	http.MethodPut + " " + apiBase + "/workspaces/{workspace_id}/objects": true,
	http.MethodPut + " " + apiBase + "/objects/{workspace_id}":            true,
}

// requestBody checks the body of every request of a route other than the
// upload routes, before the generated handler decodes it:
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
			if uploadPatterns[r.Pattern] {
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
			if !validJSONUnicode(body) {
				WriteError(w, r, NewError(CodeInvalidRequest, "the JSON body contains invalid Unicode", nil))
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

// encoding/json substitutes U+FFFD for malformed UTF-8 and lone UTF-16
// surrogates. Reject both before a path or an identity can change spelling.
// JSON syntax, including the spelling of other escapes, is checked by the decoder.
func validJSONUnicode(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\\')
		if i < 0 || len(b)-i < 2 {
			break
		}
		b = b[i+1:]
		if b[0] != 'u' || len(b) < 5 {
			b = b[1:]
			continue
		}
		n, err := strconv.ParseUint(string(b[1:5]), 16, 16)
		b = b[5:]
		if err != nil || n < 0xd800 || n > 0xdfff {
			continue
		}
		if n > 0xdbff || len(b) < 6 || b[0] != '\\' || b[1] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(b[2:6]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		b = b[6:]
	}
	return true
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
