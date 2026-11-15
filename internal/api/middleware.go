package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/actorref"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

// HeaderRequestID is the request ID header, read from requests and set on
// every response.
const HeaderRequestID = "X-Request-Id"

// MaxRequestIDLength bounds a client-supplied request ID.
const MaxRequestIDLength = 128

type requestIDKey struct{}
type principalKey struct{}
type actorSlotKey struct{}

// actorSlot lets the authentication middleware tell the access log who the
// caller is: the access log runs outside it and cannot see the request
// context that authentication derives. It is written synchronously while
// the request is served, before the access log reads it.
type actorSlot struct {
	p  ports.Principal
	ok bool
}

// RequestIDFrom returns the request ID stored by the RequestID middleware.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// PrincipalFrom returns the principal stored by the authentication
// middleware.
func PrincipalFrom(ctx context.Context) (ports.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(ports.Principal)
	return p, ok
}

// validRequestID accepts 1-128 visible ASCII characters (no spaces or
// control characters, so the value is safe in headers and logs).
func validRequestID(s string) bool {
	if len(s) == 0 || len(s) > MaxRequestIDLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// RequestID reuses a valid X-Request-Id or generates a new ID, stores it in
// the context and sets it on the response before anything else runs.
func RequestID(gen ports.IDGen) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(HeaderRequestID)
			if !validRequestID(id) {
				id = gen.NewID()
			}
			w.Header().Set(HeaderRequestID, id)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
		})
	}
}

// statusRecorder captures the response status for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// AccessLog logs one info line per request (never the query string, which
// may contain file paths). The caller appears only as "actor", the
// pseudonym of its actor reference (actorref.Pseudonym): raw actor
// references and principal UIDs are written only to a separate debug-level
// record, so they appear in the logs only when the level is debug.
func AccessLog(l *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			slot := &actorSlot{}
			ctx := context.WithValue(context.WithValue(r.Context(), loggerKey{}, l), actorSlotKey{}, slot)
			r = r.WithContext(ctx)
			next.ServeHTTP(rec, r)
			attrs := []any{"method", r.Method, "path", r.URL.Path, "status", rec.status, "bytes", rec.bytes,
				"duration_ms", time.Since(start).Milliseconds(), "request_id", RequestIDFrom(ctx)}
			if slot.ok {
				attrs = append(attrs, "actor", actorref.Pseudonym(slot.p.Ref))
			}
			l.Info("http request", attrs...)
			if slot.ok && l.Enabled(ctx, slog.LevelDebug) {
				l.Debug("http request actor", "request_id", RequestIDFrom(ctx), "actor", actorref.Pseudonym(slot.p.Ref),
					"actor_ref", slot.p.Ref, "principal_uid", slot.p.PrincipalUID)
			}
		})
	}
}

// Recover turns a panic into a 500 "internal" error response.
func Recover(l *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					if v == http.ErrAbortHandler {
						panic(v)
					}
					l.Error("panic", "request_id", RequestIDFrom(r.Context()), "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
					WriteError(w, r, NewError(CodeInternal, "internal error", nil))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// Authenticate requires valid credentials on every path except the public
// ones and stores the principal in the request context.
func Authenticate(a ports.Authenticator, public map[string]bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if public[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			p, err := a.Authenticate(r)
			if err != nil {
				WriteError(w, r, NewError(CodeUnauthenticated, "missing or invalid bearer token", nil))
				return
			}
			if slot, ok := r.Context().Value(actorSlotKey{}).(*actorSlot); ok {
				slot.p, slot.ok = p, true
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
		})
	}
}
