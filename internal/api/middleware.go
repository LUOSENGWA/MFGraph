package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"runtime/debug"
	"strings"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

// HeaderRequestID is the request ID header, read from requests and set on
// every response.
const HeaderRequestID = "X-Request-Id"

// MaxRequestIDLength bounds a client-supplied request ID.
const MaxRequestIDLength = 128

type requestIDKey struct{}
type principalKey struct{}
type claimsKey struct{}
type actorSlotKey struct{}

// actorSlot lets the authentication middleware tell the access log who the
// caller is: the access log runs outside it and cannot see the request
// context that authentication derives. It is written synchronously while
// the request is served, before the access log reads it.
type actorSlot struct {
	p      ports.Principal
	claims RequestClaims
	ok     bool
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

// ClaimsFrom returns the recorded identity headers of a request
// authenticated by TrustedHeaders (zero otherwise).
func ClaimsFrom(ctx context.Context) RequestClaims {
	c, _ := ctx.Value(claimsKey{}).(RequestClaims)
	return c
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

// RequestID reuses a valid request ID or generates a new ID, stores it in
// the context and sets it on the response (X-Request-Id) before anything
// else runs. The request ID is the first valid one of the trusted header
// (module mode: <prefix>Request-Id, set by the host platform's proxy; ""
// for none) and X-Request-Id.
func RequestID(gen ports.IDGen, trusted string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := ""
			if trusted != "" && validRequestID(r.Header.Get(trusted)) {
				id = r.Header.Get(trusted)
			}
			if id == "" {
				id = r.Header.Get(HeaderRequestID)
			}
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
// pseudonym of its actor reference (pseudonym, an
// actorref.Pseudonymizer), and so does the actor it acts on behalf of
// ("on_behalf_of"): raw actor references and principal UIDs are written
// only to a separate debug-level record, so they appear in the logs only
// when the level is debug. In module mode the line also carries the actor
// types, the project, the recorded identity headers (team, token_jti,
// audience, issued_at) and trace_id, the trace ID the host platform's
// proxy sent in traceHeader ("" for none).
func AccessLog(l *slog.Logger, pseudonym func(string) string, traceHeader string) func(http.Handler) http.Handler {
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
			if traceHeader != "" {
				if t := r.Header.Get(traceHeader); validRequestID(t) {
					attrs = append(attrs, "trace_id", t)
				}
			}
			if slot.ok {
				p, c := slot.p, slot.claims
				attrs = append(attrs, "actor", pseudonym(p.Ref))
				for _, kv := range [][2]string{{"actor_type", p.ActorType}, {"project_id", p.ProjectScope},
					{"on_behalf_of_type", p.OnBehalfOfType}, {"team", c.Team}, {"token_jti", c.TokenJTI},
					{"audience", c.Audience}, {"issued_at", c.IssuedAt}} {
					if kv[1] != "" {
						attrs = append(attrs, kv[0], kv[1])
					}
				}
				if p.OnBehalfOfRef != "" {
					attrs = append(attrs, "on_behalf_of", pseudonym(p.OnBehalfOfRef))
				}
			}
			l.Info("http request", attrs...)
			if slot.ok && l.Enabled(ctx, slog.LevelDebug) {
				l.Debug("http request actor", "request_id", RequestIDFrom(ctx), "actor", pseudonym(slot.p.Ref),
					"actor_ref", slot.p.Ref, "principal_uid", slot.p.PrincipalUID, "on_behalf_of_ref", slot.p.OnBehalfOfRef)
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
// ones and stores the principal (and the recorded identity headers of
// TrustedHeaders) in the request context. Every failure is 401
// unauthenticated; identity headers that TrustedHeaders refuses after a
// valid internal token are also logged as a warning (header name and rule,
// never the value).
func Authenticate(a ports.Authenticator, public map[string]bool) func(http.Handler) http.Handler {
	claims, _ := a.(interface {
		Claims(*http.Request) RequestClaims
	})
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if public[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			p, err := a.Authenticate(r)
			if err != nil {
				e := NewError(CodeUnauthenticated, "missing or invalid bearer token", nil)
				var ae *authError
				if errors.As(err, &ae) {
					e.Message, e.noChallenge = ae.msg, true
					if ae.claim {
						logger(r).Warn("identity headers refused", "request_id", RequestIDFrom(r.Context()), "reason", ae.msg)
					}
				}
				WriteError(w, r, e)
				return
			}
			var rc RequestClaims
			if claims != nil {
				rc = claims.Claims(r)
			}
			if slot, ok := r.Context().Value(actorSlotKey{}).(*actorSlot); ok {
				slot.p, slot.claims, slot.ok = p, rc, true
			}
			ctx := context.WithValue(context.WithValue(r.Context(), principalKey{}, p), claimsKey{}, rc)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// apiBase is the path of the business endpoints in the contract.
const apiBase = "/api/v1"

// Prefix serves the business endpoints below prefix instead of /api/v1: a
// request to <prefix>/<rest> is routed as /api/v1/<rest>, and any other
// path except the public ones gives a JSON 404. The access log keeps the
// path as requested.
//
// A path that is not in its clean form (an empty, "." or ".." segment, a
// trailing '/', also when percent-encoded) gives the same 404, in both
// modes and with any prefix: the router would otherwise redirect it to its
// clean form below /api/v1, a path that behind a proxy with another prefix
// leads elsewhere. The public paths (/healthz, /readyz) are not affected.
func Prefix(prefix string, public map[string]bool) func(http.Handler) http.Handler {
	if prefix == "" {
		prefix = apiBase
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if public[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			if p := r.URL.Path; p != path.Clean(p) {
				WriteError(w, r, NewError(CodeNotFound, "no such endpoint", nil))
				return
			}
			if prefix == apiBase {
				next.ServeHTTP(w, r)
				return
			}
			esc := r.URL.EscapedPath()
			rest, ok := strings.CutPrefix(esc, prefix)
			if !ok || rest != "" && rest[0] != '/' {
				WriteError(w, r, NewError(CodeNotFound, "no such endpoint", nil))
				return
			}
			path, err := url.PathUnescape(apiBase + rest)
			if err != nil {
				WriteError(w, r, NewError(CodeNotFound, "no such endpoint", nil))
				return
			}
			u := *r.URL
			u.Path, u.RawPath = path, apiBase+rest
			r2 := r.Clone(r.Context())
			r2.URL = &u
			next.ServeHTTP(w, r2)
		})
	}
}

type contentLengthKey struct{}

// ContentLengthFrom returns the Content-Length of the request (-1 when
// unknown), stored by NewHandler for the upload endpoint.
func ContentLengthFrom(ctx context.Context) int64 {
	if n, ok := ctx.Value(contentLengthKey{}).(int64); ok {
		return n
	}
	return -1
}
