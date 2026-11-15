package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/api/gen"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

// ReadinessCheck is one dependency checked by /readyz.
type ReadinessCheck struct {
	Name  string                          // e.g. "db"
	Code  string                          // error code when the check fails, e.g. CodeDBUnavailable
	Check func(ctx context.Context) error // nil means "not configured"
}

// Options configures NewHandler.
type Options struct {
	Authenticator ports.Authenticator
	IDGen         ports.IDGen
	Readiness     []ReadinessCheck
	Logger        *slog.Logger
}

// publicPaths are served without authentication.
var publicPaths = map[string]bool{"/healthz": true, "/readyz": true}

// NewHandler returns the HTTP handler: generated routes on a ServeMux, a
// JSON 404 for everything else, and the middleware chain request ID ->
// access log -> panic recovery -> authentication.
func NewHandler(o Options) http.Handler {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	strict := gen.NewStrictHandlerWithOptions(&handlers{readiness: o.Readiness}, nil, gen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			WriteError(w, r, NewError(CodeInvalidRequest, err.Error(), nil))
		},
		ResponseErrorHandlerFunc: WriteError,
	})
	mux := http.NewServeMux()
	gen.HandlerWithOptions(strict, gen.StdHTTPServerOptions{
		BaseRouter: mux,
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			WriteError(w, r, NewError(CodeInvalidRequest, err.Error(), nil))
		},
	})
	// Unknown paths and unsupported methods on known paths.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, NewError(CodeNotFound, "no such endpoint", nil))
	})
	var h http.Handler = mux
	h = Authenticate(o.Authenticator, publicPaths)(h)
	h = Recover(o.Logger)(h)
	h = AccessLog(o.Logger)(h)
	h = RequestID(o.IDGen)(h)
	return h
}

// handlers implements the generated strict server interface. Only the health
// endpoints are implemented in this build; every other operation answers
// 501 not_implemented.
type handlers struct {
	readiness []ReadinessCheck
}

var _ gen.StrictServerInterface = (*handlers)(nil)

func (h *handlers) GetHealthz(context.Context, gen.GetHealthzRequestObject) (gen.GetHealthzResponseObject, error) {
	return gen.GetHealthz200JSONResponse{Status: gen.Ok}, nil
}

func (h *handlers) GetReadyz(ctx context.Context, _ gen.GetReadyzRequestObject) (gen.GetReadyzResponseObject, error) {
	checks := map[string]string{}
	for _, c := range h.readiness {
		if c.Check == nil {
			checks[c.Name] = "not_configured"
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := c.Check(cctx)
		cancel()
		if err != nil {
			id := RequestIDFrom(ctx)
			details := map[string]any{"check": c.Name}
			return gen.GetReadyz503JSONResponse{ServiceUnavailableJSONResponse: gen.ServiceUnavailableJSONResponse{
				Code: c.Code, Message: c.Name + " is not available", RequestId: &id, Details: &details}}, nil
		}
		checks[c.Name] = "ok"
	}
	return gen.GetReadyz200JSONResponse{Status: gen.Ready, Checks: checks}, nil
}

func (h *handlers) CreateProject(context.Context, gen.CreateProjectRequestObject) (gen.CreateProjectResponseObject, error) {
	return nil, notImplemented("createProject")
}

func (h *handlers) GetProject(context.Context, gen.GetProjectRequestObject) (gen.GetProjectResponseObject, error) {
	return nil, notImplemented("getProject")
}

func (h *handlers) CreateWorkspace(context.Context, gen.CreateWorkspaceRequestObject) (gen.CreateWorkspaceResponseObject, error) {
	return nil, notImplemented("createWorkspace")
}

func (h *handlers) GetWorkspace(context.Context, gen.GetWorkspaceRequestObject) (gen.GetWorkspaceResponseObject, error) {
	return nil, notImplemented("getWorkspace")
}

func (h *handlers) ListBranches(context.Context, gen.ListBranchesRequestObject) (gen.ListBranchesResponseObject, error) {
	return nil, notImplemented("listBranches")
}

func (h *handlers) PutObject(context.Context, gen.PutObjectRequestObject) (gen.PutObjectResponseObject, error) {
	return nil, notImplemented("putObject")
}

func (h *handlers) CreateCommit(context.Context, gen.CreateCommitRequestObject) (gen.CreateCommitResponseObject, error) {
	return nil, notImplemented("createCommit")
}

func (h *handlers) GetTimeline(context.Context, gen.GetTimelineRequestObject) (gen.GetTimelineResponseObject, error) {
	return nil, notImplemented("getTimeline")
}

func (h *handlers) GetSnapshot(context.Context, gen.GetSnapshotRequestObject) (gen.GetSnapshotResponseObject, error) {
	return nil, notImplemented("getSnapshot")
}

func (h *handlers) ListSnapshotTree(context.Context, gen.ListSnapshotTreeRequestObject) (gen.ListSnapshotTreeResponseObject, error) {
	return nil, notImplemented("listSnapshotTree")
}

func (h *handlers) GetSnapshotFile(context.Context, gen.GetSnapshotFileRequestObject) (gen.GetSnapshotFileResponseObject, error) {
	return nil, notImplemented("getSnapshotFile")
}

func (h *handlers) GetSnapshotDiff(context.Context, gen.GetSnapshotDiffRequestObject) (gen.GetSnapshotDiffResponseObject, error) {
	return nil, notImplemented("getSnapshotDiff")
}

func (h *handlers) RestoreSnapshot(context.Context, gen.RestoreSnapshotRequestObject) (gen.RestoreSnapshotResponseObject, error) {
	return nil, notImplemented("restoreSnapshot")
}
