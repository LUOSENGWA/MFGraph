package api_test

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/api"
	"github.com/LUOSENGWA/MFGraph/internal/core"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

// TestRetryableDBErrors: lock wait timeouts and deadlocks are 503
// db_unavailable with details.reason and Retry-After: 1, whether they come
// from a port or through the core; other database errors have no
// Retry-After.
func TestRetryableDBErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		reason any
		retry  string
	}{
		{fmt.Errorf("mysql: %w", ports.ErrLockTimeout), core.ReasonLockTimeout, "1"},
		{fmt.Errorf("mysql: %w", ports.ErrDeadlock), core.ReasonDeadlock, "1"},
		{&core.Error{Code: core.CodeDBUnavailable, Message: "m", Details: map[string]any{"reason": core.ReasonDeadlock}},
			core.ReasonDeadlock, "1"},
		{fmt.Errorf("mysql: %w", ports.ErrDBUnavailable), nil, ""},
		{&core.Error{Code: core.CodeDBUnavailable, Message: "m", Details: map[string]any{"reason": "not_configured"}},
			"not_configured", ""},
	} {
		rec := httptest.NewRecorder()
		api.WriteError(rec, httptest.NewRequest("POST", "/api/v1/projects", nil), tc.err)
		var e envelope
		if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		if rec.Code != 503 || e.Code != api.CodeDBUnavailable || e.Details["reason"] != tc.reason ||
			rec.Header().Get("Retry-After") != tc.retry {
			t.Errorf("%v: %d %+v, Retry-After %q", tc.err, rec.Code, e, rec.Header().Get("Retry-After"))
		}
	}
}
