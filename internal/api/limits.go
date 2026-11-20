package api

import (
	"errors"
	"io"
	"net/http"
	"os"
	"time"
)

type timedUploadBody struct{ io.ReadCloser }

func (b timedUploadBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return n, &Error{Status: http.StatusRequestTimeout, Code: CodeInvalidRequest, Message: "upload body did not finish within the configured duration", Details: map[string]any{"reason": "upload_timeout"}}
	}
	return n, err
}

func slots(capacity int) chan struct{} {
	if capacity <= 0 {
		return nil
	}
	return make(chan struct{}, capacity)
}

// limitWork uses separate process-wide pools so active upload bodies and
// expensive directory operations cannot consume each other's slots. Health
// and lightweight metadata requests stay available.
func limitWork(o Options) func(http.Handler) http.Handler {
	uploads, trees := slots(o.UploadConcurrency), slots(o.TreeConcurrency)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			streaming := uploadPatterns[r.Pattern]
			var pool chan struct{}
			switch {
			case streaming || r.Pattern == "POST "+apiBase+"/uploads/{upload_id}/complete":
				pool = uploads
			case r.Pattern == "GET "+apiBase+"/snapshots/{snapshot_id}/tree", r.Pattern == "GET "+apiBase+"/snapshots/{snapshot_id}/diff",
				r.Pattern == "POST "+apiBase+"/workspaces/{workspace_id}/commits", r.Pattern == "POST "+apiBase+"/snapshots/{snapshot_id}/restore":
				pool = trees
			}
			if pool != nil {
				select {
				case pool <- struct{}{}:
					defer func() { <-pool }()
				default:
					if r.ContentLength != 0 {
						w.Header().Set("Connection", "close")
					}
					w.Header().Set("Retry-After", "1")
					WriteError(w, r, NewError(CodeRateLimited, "all request slots for this operation are in use", nil))
					return
				}
			}
			if streaming && o.UploadTimeout > 0 {
				controller := http.NewResponseController(w)
				if err := controller.SetReadDeadline(time.Now().Add(o.UploadTimeout)); err != nil {
					WriteError(w, r, err)
					return
				}
				// Leave this deadline in force while net/http drains unread body bytes.
				// The server installs its idle/header deadline for the next request.
				r.Body = timedUploadBody{r.Body}
			}
			next.ServeHTTP(w, r)
		})
	}
}
