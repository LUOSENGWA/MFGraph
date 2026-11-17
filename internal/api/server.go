package api

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/actorref"
	"github.com/LUOSENGWA/MFGraph/internal/api/gen"
	"github.com/LUOSENGWA/MFGraph/internal/core"
	"github.com/LUOSENGWA/MFGraph/internal/pathx"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

// ReadinessCheck is one dependency checked by /readyz.
type ReadinessCheck struct {
	Name string // e.g. "db"
	Code string // error code when the check fails, e.g. CodeDBUnavailable
	// Check reports whether the dependency works; nil means "not
	// configured".
	Check func(ctx context.Context) error
	// Required makes an unconfigured dependency fail the check (503 with
	// details.reason not_configured) instead of reporting not_configured.
	Required bool
}

// Options configures NewHandler.
type Options struct {
	Authenticator ports.Authenticator
	IDGen         ports.IDGen
	Readiness     []ReadinessCheck
	Logger        *slog.Logger
	// Service runs the business endpoints; without one (no database) they
	// answer 503 db_unavailable.
	Service *core.Service
	// APIPrefix is the path prefix of the business endpoints ("" = /api/v1).
	APIPrefix string
	// Version is reported by /readyz.
	Version string
	// Pseudonym maps actor references to their pseudonyms in the logs; nil
	// uses an actorref.Pseudonymizer with a random key.
	Pseudonym func(string) string
	// MaxJSONBodyBytes bounds the body of every request but an upload
	// (0 = DefaultMaxJSONBodyBytes, 16 MiB).
	MaxJSONBodyBytes int64
	// TrustedHeaderPrefix is the prefix of the identity headers in module
	// mode ("" in standalone mode): the request ID is taken from
	// <prefix>Request-Id first, and <prefix>Trace-Id is logged as trace_id.
	TrustedHeaderPrefix string
	// NoProjects removes the project endpoints (module mode: the host
	// platform owns the projects): /projects and /projects/{project_id}
	// answer 404 as unknown paths, whatever the method and the body.
	NoProjects bool
	// Outbox reports the delivery of events to the host platform's outbox
	// table (module mode; nil without a forwarder): /readyz shows it in the
	// field outbox and, when delivery is stalled, as checks.outbox
	// "stalled". A stall does not make the server unready.
	Outbox func(ctx context.Context) (OutboxState, error)
}

// OutboxState is the delivery state Options.Outbox reports. When the
// backlog cannot be read, Outbox returns an error with the rest.
type OutboxState struct {
	Pending         int64
	OldestPendingAt time.Time // zero: nothing pending
	Stalled         bool
	StalledSince    time.Time
	LastError       string
}

// publicPaths are served without authentication.
var publicPaths = map[string]bool{"/healthz": true, "/readyz": true}

// NewHandler returns the HTTP handler: generated routes on a ServeMux, a
// JSON 404 for everything else, and the middleware chain request ID ->
// access log -> panic recovery -> authentication -> API prefix; every
// route but the upload checks its request body first (requestBody).
func NewHandler(o Options) http.Handler {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Pseudonym == nil {
		key := make([]byte, 32)
		_, _ = rand.Read(key)
		p, _ := actorref.NewPseudonymizer(key)
		o.Pseudonym = p.Pseudonym
	}
	h := &handlers{svc: o.Service, readiness: o.Readiness, version: o.Version, outbox: o.Outbox, logger: o.Logger}
	strict := gen.NewStrictHandlerWithOptions(h, []gen.StrictMiddlewareFunc{contentLength}, gen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			WriteError(w, r, NewError(CodeInvalidRequest, err.Error(), nil))
		},
		ResponseErrorHandlerFunc: WriteError,
	})
	mux := http.NewServeMux()
	gen.HandlerWithOptions(strict, gen.StdHTTPServerOptions{
		BaseRouter:  mux,
		Middlewares: []gen.MiddlewareFunc{requestBody(o.MaxJSONBodyBytes)},
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			WriteError(w, r, NewError(CodeInvalidRequest, err.Error(), nil))
		},
	})
	// Unknown paths and unsupported methods on known paths.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, NewError(CodeNotFound, "no such endpoint", nil))
	})
	var handler http.Handler = mux
	if o.NoProjects {
		handler = withoutProjects(handler)
	}
	handler = Prefix(o.APIPrefix, publicPaths)(handler)
	handler = Authenticate(o.Authenticator, publicPaths)(handler)
	handler = Recover(o.Logger)(handler)
	var requestIDHeader, traceHeader string
	if o.TrustedHeaderPrefix != "" {
		requestIDHeader, traceHeader = o.TrustedHeaderPrefix+ClaimRequestID, o.TrustedHeaderPrefix+ClaimTraceID
	}
	handler = AccessLog(o.Logger, o.Pseudonym, traceHeader)(handler)
	handler = RequestID(o.IDGen, requestIDHeader)(handler)
	return handler
}

// withoutProjects answers the project endpoints with the 404 of an unknown
// path (Options.NoProjects).
func withoutProjects(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.URL.Path; p == apiBase+"/projects" || strings.HasPrefix(p, apiBase+"/projects/") {
			WriteError(w, r, NewError(CodeNotFound, "no such endpoint", nil))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// contentLength passes the request's Content-Length to the upload handlers,
// which refuse a body above the limit before reading it.
func contentLength(f gen.StrictHandlerFunc, operationID string) gen.StrictHandlerFunc {
	if operationID != "PutObject" && operationID != "UploadObject" {
		return f
	}
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request interface{}) (interface{}, error) {
		return f(context.WithValue(ctx, contentLengthKey{}, r.ContentLength), w, r, request)
	}
}

// handlers implements the generated strict server interface on top of
// core.Service.
type handlers struct {
	svc       *core.Service
	readiness []ReadinessCheck
	version   string
	outbox    func(ctx context.Context) (OutboxState, error)
	logger    *slog.Logger
}

var _ gen.StrictServerInterface = (*handlers)(nil)

func (h *handlers) GetHealthz(context.Context, gen.GetHealthzRequestObject) (gen.GetHealthzResponseObject, error) {
	return gen.GetHealthz200JSONResponse{Status: gen.Ok}, nil
}

func (h *handlers) GetReadyz(ctx context.Context, _ gen.GetReadyzRequestObject) (gen.GetReadyzResponseObject, error) {
	checks := map[string]string{}
	id := RequestIDFrom(ctx)
	fail := func(c ReadinessCheck, message string, details map[string]any) gen.GetReadyzResponseObject {
		details["check"] = c.Name
		return gen.GetReadyz503JSONResponse{ServiceUnavailableJSONResponse: gen.ServiceUnavailableJSONResponse{
			Code: c.Code, Message: message, RequestId: &id, Details: &details}}
	}
	for _, c := range h.readiness {
		if c.Check == nil {
			if c.Required {
				return fail(c, c.Name+" is not configured", map[string]any{"reason": "not_configured"}), nil
			}
			checks[c.Name] = "not_configured"
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := c.Check(cctx)
		cancel()
		if err != nil {
			return fail(c, c.Name+" is not available", map[string]any{}), nil
		}
		checks[c.Name] = "ok"
	}
	uv, v := pathx.UnicodeVersion, h.version
	resp := gen.GetReadyz200JSONResponse{Status: gen.Ready, Checks: checks, UnicodeVersion: &uv}
	if v != "" {
		resp.Version = &v
	}
	if h.outbox != nil {
		resp.Outbox = h.outboxReport(ctx, checks)
	}
	return resp, nil
}

// outboxReport returns the field outbox of /readyz and sets checks.outbox
// to "stalled" when delivery is stalled (the server stays ready).
func (h *handlers) outboxReport(ctx context.Context, checks map[string]string) *gen.ReadinessOutbox {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	st, err := h.outbox(cctx)
	cancel()
	if st.Stalled {
		checks["outbox"] = "stalled"
	} else if _, ok := checks["outbox"]; !ok {
		checks["outbox"] = "ok"
	}
	var out gen.ReadinessOutbox
	if err == nil {
		out.Pending = &st.Pending
		if !st.OldestPendingAt.IsZero() {
			t := st.OldestPendingAt.UTC()
			out.OldestPendingAt = &t
		}
	} else {
		h.logger.Warn("readyz: cannot count the pending events of the outbox", "request_id", RequestIDFrom(ctx), "err", err.Error())
	}
	if st.Stalled {
		since, last := st.StalledSince.UTC(), st.LastError
		out.StalledSince, out.LastError = &since, &last
	}
	if out == (gen.ReadinessOutbox{}) {
		return nil
	}
	return &out
}
