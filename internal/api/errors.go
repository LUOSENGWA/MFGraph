package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/LUOSENGWA/MFGraph/internal/api/gen"
	"github.com/LUOSENGWA/MFGraph/internal/core"
	"github.com/LUOSENGWA/MFGraph/internal/pathx"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

// Stable error codes (contract: components/schemas/Error in the OpenAPI
// document).
const (
	CodeInvalidRequest      = "invalid_request"
	CodeUnauthenticated     = "unauthenticated"
	CodeForbidden           = "forbidden"
	CodeNotFound            = "not_found"
	CodeConflict            = "conflict"
	CodeIdempotencyKeyReuse = "idempotency_key_reuse"
	CodeUploadExpired       = "upload_expired"
	CodeBlobMissing         = "blob_missing"
	CodePreconditionFailed  = "precondition_failed"
	CodeQuotaExceeded       = "quota_exceeded"
	CodeFileTooLarge        = "file_too_large"
	CodeRequestTooLarge     = "request_too_large"
	CodeInvalidPath         = "invalid_path"
	CodeUnsupportedFormat   = "unsupported_format"
	CodeRateLimited         = "rate_limited"
	CodeInternal            = "internal"
	CodeNotImplemented      = "not_implemented"
	CodeStorageUnavailable  = "storage_unavailable"
	CodeDBUnavailable       = "db_unavailable"
)

// codeStatus is the HTTP status of every code.
var codeStatus = map[string]int{
	CodeInvalidRequest:      http.StatusBadRequest,
	CodeUnauthenticated:     http.StatusUnauthorized,
	CodeForbidden:           http.StatusForbidden,
	CodeNotFound:            http.StatusNotFound,
	CodeConflict:            http.StatusConflict,
	CodeIdempotencyKeyReuse: http.StatusConflict,
	CodeUploadExpired:       http.StatusGone,
	CodeBlobMissing:         http.StatusGone,
	CodePreconditionFailed:  http.StatusPreconditionFailed,
	CodeQuotaExceeded:       http.StatusRequestEntityTooLarge,
	CodeFileTooLarge:        http.StatusRequestEntityTooLarge,
	CodeRequestTooLarge:     http.StatusRequestEntityTooLarge,
	CodeInvalidPath:         http.StatusUnprocessableEntity,
	CodeUnsupportedFormat:   http.StatusUnprocessableEntity,
	CodeRateLimited:         http.StatusTooManyRequests,
	CodeInternal:            http.StatusInternalServerError,
	CodeNotImplemented:      http.StatusNotImplemented,
	CodeStorageUnavailable:  http.StatusServiceUnavailable,
	CodeDBUnavailable:       http.StatusServiceUnavailable,
}

// StatusOf returns the HTTP status of a code (500 for unknown codes).
func StatusOf(code string) int {
	if s, ok := codeStatus[code]; ok {
		return s
	}
	return http.StatusInternalServerError
}

// Error is an error with a stable API code.
type Error struct {
	Status  int // 0 = StatusOf(Code)
	Code    string
	Message string
	Details map[string]any
	cause   error
	// noChallenge leaves the WWW-Authenticate header off a 401: the
	// identity headers of module mode are no bearer scheme.
	noChallenge bool
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return e.Code + ": " + e.Message
}

// Unwrap returns the underlying error, if any.
func (e *Error) Unwrap() error { return e.cause }

// NewError returns an API error; the status follows from the code.
func NewError(code, message string, details map[string]any) *Error {
	return &Error{Code: code, Message: message, Details: details}
}

// toAPIError maps any error to an API error. Unknown errors become
// "internal" without leaking their text.
func toAPIError(err error) (*Error, bool) {
	var ae *Error
	var ce *core.Error
	switch {
	case errors.As(err, &ae):
		return ae, true
	case errors.As(err, &ce):
		if _, known := codeStatus[ce.Code]; !known {
			return &Error{Code: CodeInternal, Message: "internal error", cause: err}, false
		}
		return &Error{Code: ce.Code, Message: ce.Message, Details: ce.Details, cause: err}, true
	case errors.Is(err, pathx.ErrInvalidPath):
		return &Error{Code: CodeInvalidPath, Message: err.Error(),
			Details: map[string]any{"kind": string(pathx.KindOf(err))}, cause: err}, true
	case errors.Is(err, ports.ErrUnauthenticated):
		return &Error{Code: CodeUnauthenticated, Message: "missing or invalid bearer token", cause: err}, true
	case errors.Is(err, ports.ErrNotFound):
		return &Error{Code: CodeNotFound, Message: "not found", cause: err}, true
	case errors.Is(err, ports.ErrForbidden):
		return &Error{Code: CodeForbidden, Message: "forbidden", cause: err}, true
	case errors.Is(err, ports.ErrDeadlock):
		return &Error{Code: CodeDBUnavailable, Message: "the database rolled this request back to end a deadlock; repeat it",
			Details: map[string]any{"reason": core.ReasonDeadlock}, cause: err}, true
	case errors.Is(err, ports.ErrLockTimeout):
		return &Error{Code: CodeDBUnavailable, Message: "the database did not grant a lock in time; repeat the request",
			Details: map[string]any{"reason": core.ReasonLockTimeout}, cause: err}, true
	case errors.Is(err, ports.ErrDBUnavailable):
		return &Error{Code: CodeDBUnavailable, Message: "the database is not available", cause: err}, true
	case errors.Is(err, ports.ErrStorageUnavailable):
		return &Error{Code: CodeStorageUnavailable, Message: "the storage is not available", cause: err}, true
	}
	return &Error{Code: CodeInternal, Message: "internal error", cause: err}, false
}

// WriteError writes err as the flat error envelope
// {"code","message","request_id","details"} with the matching status.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	ae, known := toAPIError(err)
	switch {
	case !known:
		logger(r).Error("internal error", "request_id", RequestIDFrom(r.Context()), "err", err)
	case ae.Code == CodeDBUnavailable || ae.Code == CodeStorageUnavailable:
		logger(r).Warn("dependency unavailable", "request_id", RequestIDFrom(r.Context()), "code", ae.Code, "err", err)
	}
	status := ae.Status
	if status == 0 {
		status = StatusOf(ae.Code)
	}
	body := gen.Error{Code: ae.Code, Message: ae.Message}
	if id := RequestIDFrom(r.Context()); id != "" {
		body.RequestId = &id
	}
	if len(ae.Details) > 0 {
		d := ae.Details
		body.Details = &d
	}
	if status == http.StatusRequestTimeout {
		w.Header().Set("Connection", "close")
	}
	if status == http.StatusUnauthorized && !ae.noChallenge {
		w.Header().Set("WWW-Authenticate", `Bearer realm="mfgraph"`)
	}
	if retryable(ae) {
		w.Header().Set("Retry-After", "1")
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// retryable reports the errors after which the same request can be
// repeated at once: a lock wait timeout or a deadlock in the database.
func retryable(ae *Error) bool {
	if ae.Code != CodeDBUnavailable {
		return false
	}
	r := ae.Details["reason"]
	return r == core.ReasonLockTimeout || r == core.ReasonDeadlock
}

type loggerKey struct{}

func logger(r *http.Request) *slog.Logger {
	if l, ok := r.Context().Value(loggerKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
