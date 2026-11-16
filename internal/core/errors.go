package core

import (
	"errors"
	"fmt"
)

// Error codes. They are the stable codes of the HTTP API (contract:
// components/schemas/Error), which maps them to statuses; the core does not
// know HTTP.
const (
	CodeInvalidRequest      = "invalid_request"
	CodeForbidden           = "forbidden"
	CodeNotFound            = "not_found"
	CodeConflict            = "conflict"
	CodeIdempotencyKeyReuse = "idempotency_key_reuse"
	CodeBlobMissing         = "blob_missing"
	CodeFileTooLarge        = "file_too_large"
	CodeRangeNotSatisfiable = "range_not_satisfiable" // the API answers 416 with code invalid_request
	CodeDBUnavailable       = "db_unavailable"
	CodeStorageUnavailable  = "storage_unavailable"
)

// Values of details.reason.
const (
	ReasonChecksumMismatch = "checksum_mismatch"
	ReasonNoChanges        = "no_changes"
	ReasonPathNotFound     = "path_not_found"
	ReasonInProgress       = "in_progress"
	ReasonNotUploaded      = "not_uploaded"
	ReasonMissingInStorage = "missing_in_storage"
	ReasonNotADirectory    = "not_a_directory"
	ReasonNotAFile         = "not_a_file"
	ReasonTooManyChanges   = "too_many_changes"
	// ReasonLockTimeout and ReasonDeadlock (db_unavailable): concurrent
	// requests held the rows this one needed; nothing was changed and the
	// request can be repeated as it is.
	ReasonLockTimeout = "lock_timeout"
	ReasonDeadlock    = "deadlock"
)

// Error is a failure with a stable code and machine-readable details.
// Errors of the path rules are *pathx.Error values instead (the API maps
// them to invalid_path).
type Error struct {
	Code    string
	Message string
	Details map[string]any
	Err     error // cause, not shown to clients
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return e.Code + ": " + e.Message
}

// Unwrap returns the cause.
func (e *Error) Unwrap() error { return e.Err }

func newError(code, message string, details map[string]any) *Error {
	return &Error{Code: code, Message: message, Details: details}
}

func invalid(message string) *Error { return newError(CodeInvalidRequest, message, nil) }

func invalidReason(reason, message string) *Error {
	return newError(CodeInvalidRequest, message, map[string]any{"reason": reason})
}

func notFound(what string) *Error { return newError(CodeNotFound, what+" not found", nil) }

// CodeOf returns the code of err if it is (or wraps) an *Error, else "".
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
