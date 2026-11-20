package core

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/pathx"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/tree"
)

// Defaults of Config.
const (
	DefaultStorageBackendID = "default"
	DefaultMaxUploadBytes   = 1 << 30
	DefaultMaxCommitChanges = 10_000
	DefaultIdempotencyTTL   = 24 * time.Hour
	DefaultIdempotencyLease = 60 * time.Second
	DefaultLimit            = 50
	MaxLimit                = 500
	// MaxIdempotencyKey bounds an Idempotency-Key (contract: 1-128 visible
	// ASCII characters).
	MaxIdempotencyKey = 128
)

// Config holds the settings of the use cases.
type Config struct {
	// TenantID is the tenant of every project and workspace (a ULID; in a
	// standalone deployment MFGRAPH_TENANT_ID).
	TenantID string
	// StorageBackendID names the storage backend that new workspaces use.
	StorageBackendID string
	// MaxUploadBytes bounds one upload (file_too_large above it).
	MaxUploadBytes int64
	// MaxCommitChanges bounds the changes of one commit.
	MaxCommitChanges int
	// IdempotencyTTL is how long an idempotency record is kept.
	IdempotencyTTL time.Duration
	// IdempotencyLease is how long a request holds its in-progress
	// idempotency record: a request with the same key that comes later
	// takes the record over and runs (default 60s).
	IdempotencyLease time.Duration
	// HostOwnsProjects is set in module mode: the host platform owns the
	// projects and their access control, and the projects table does not
	// exist. The core then never reads or writes projects: CreateProject
	// and GetProject answer not_found as for an unknown endpoint, and a
	// workspace is created in the project of the request as given (the
	// authorizer checks it; its existence is the host's business).
	HostOwnsProjects bool
}

// Deps are the ports the use cases run on.
type Deps struct {
	Store   ports.Store
	Objects ports.BlobStore // file contents
	Trees   ports.TreeStore // tree nodes
	Authz   ports.Authorizer
	Clock   ports.Clock
	IDs     ports.IDGen
	// Logger receives warnings about stored data (nil: slog.Default()).
	Logger *slog.Logger
	// EventsCommitted, if set, is called after a change committed outbox
	// events (module mode: it wakes the forwarder). It must not block.
	EventsCommitted func()
}

// Service implements the use cases of the first core slice: projects,
// workspaces with a main branch, uploads, commits, browsing, downloads,
// diffs and restores. It is safe for concurrent use.
type Service struct {
	cfg Config
	d   Deps
}

// New returns a Service; zero settings get their defaults.
func New(cfg Config, d Deps) (*Service, error) {
	if !isULID(cfg.TenantID) {
		return nil, fmt.Errorf("core: tenant ID %q is not a ULID", cfg.TenantID)
	}
	if cfg.StorageBackendID == "" {
		cfg.StorageBackendID = DefaultStorageBackendID
	}
	if cfg.MaxUploadBytes <= 0 {
		cfg.MaxUploadBytes = DefaultMaxUploadBytes
	}
	if cfg.MaxCommitChanges <= 0 {
		cfg.MaxCommitChanges = DefaultMaxCommitChanges
	}
	if cfg.IdempotencyTTL <= 0 {
		cfg.IdempotencyTTL = DefaultIdempotencyTTL
	}
	if cfg.IdempotencyLease <= 0 {
		cfg.IdempotencyLease = DefaultIdempotencyLease
	}
	if d.Store == nil || d.Objects == nil || d.Trees == nil || d.Authz == nil || d.Clock == nil || d.IDs == nil {
		return nil, errors.New("core: missing dependency")
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Service{cfg: cfg, d: d}, nil
}

// Config returns the effective settings.
func (s *Service) Config() Config { return s.cfg }

func (s *Service) nowUs() int64 { return s.d.Clock.Now().UnixMicro() }

// eventsCommitted reports committed outbox events.
func (s *Service) eventsCommitted() {
	if s.d.EventsCommitted != nil {
		s.d.EventsCommitted()
	}
}

// isULID reports a canonical ULID (26 uppercase Crockford base32
// characters, the first one 0-7).
func isULID(id string) bool {
	if len(id) != 26 || id[0] > '7' {
		return false
	}
	for i := 0; i < len(id); i++ {
		if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", rune(id[i])) {
			return false
		}
	}
	return true
}

func checkID(name, id string) error {
	if !isULID(id) {
		return invalid(name + " must be a ULID (26 uppercase Crockford base32 characters)")
	}
	return nil
}

func checkLength(name, s string, max int) error {
	if !utf8.ValidString(s) {
		return invalid(name + " is not valid UTF-8")
	}
	if n := utf8.RuneCountInString(s); n > max {
		return invalid(fmt.Sprintf("%s has %d characters, at most %d are allowed", name, n, max))
	}
	return nil
}

// maxLabelBytes bounds a label (the name of a project, the display name of
// a workspace).
const maxLabelBytes = 255

// checkLabel checks a label: valid UTF-8 of at most 255 bytes, without
// the characters pathx.IsLabelChar refuses; a required label must not be
// empty or only white space. The label is stored as given.
func checkLabel(field, s string, required bool) error {
	if !utf8.ValidString(s) {
		return invalid(field + " is not valid UTF-8")
	}
	if len(s) > maxLabelBytes {
		return invalid(fmt.Sprintf("%s has %d bytes, at most %d are allowed", field, len(s), maxLabelBytes))
	}
	if required && strings.TrimSpace(s) == "" {
		return invalid(field + " must not be empty or only white space")
	}
	for _, r := range s {
		if !pathx.IsLabelChar(r) {
			return invalid(fmt.Sprintf("%s must not contain U+%04X (control characters, noncharacters, bidi controls and invisible characters are not allowed)", field, r))
		}
	}
	return nil
}

// mapErr turns the errors of ports into core errors; other errors pass.
func mapErr(err error, what string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ports.ErrNotFound):
		return &Error{Code: CodeNotFound, Message: what + " not found", Err: err}
	case errors.Is(err, ports.ErrDeadlock):
		return &Error{Code: CodeDBUnavailable, Message: "the database rolled this request back to end a deadlock; repeat it",
			Details: map[string]any{"reason": ReasonDeadlock}, Err: err}
	case errors.Is(err, ports.ErrLockTimeout):
		return &Error{Code: CodeDBUnavailable, Message: "the database did not grant a lock in time; repeat the request",
			Details: map[string]any{"reason": ReasonLockTimeout}, Err: err}
	case errors.Is(err, ports.ErrDBUnavailable):
		return &Error{Code: CodeDBUnavailable, Message: "the database is not available", Err: err}
	case errors.Is(err, ports.ErrStorageUnavailable):
		return &Error{Code: CodeStorageUnavailable, Message: "the storage is not available", Err: err}
	}
	return err
}

// access resolves a workspace and its project and checks action. A
// workspace that does not exist and one the caller may not see both give
// not_found. In standalone mode the project is read for its owner; when the
// host owns the projects (HostOwnsProjects) it is not read at all.
func (s *Service) access(ctx context.Context, p ports.Principal, workspaceID string, action ports.Action) (ports.Workspace, ports.Project, error) {
	ws, err := s.d.Store.GetWorkspace(ctx, nil, workspaceID)
	if err != nil {
		return ports.Workspace{}, ports.Project{}, mapErr(err, "workspace")
	}
	proj := ports.Project{ID: ws.ProjectID, TenantID: ws.TenantID}
	if !s.cfg.HostOwnsProjects {
		if proj, err = s.d.Store.GetProject(ctx, nil, ws.ProjectID); err != nil {
			return ports.Workspace{}, ports.Project{}, mapErr(err, "workspace")
		}
	}
	res := ports.Resource{Type: ports.ResourceWorkspace, ID: ws.ID, TenantID: ws.TenantID, ProjectID: ws.ProjectID,
		WorkspaceID: ws.ID, OwnerRef: proj.OwnerRef}
	if err := s.check(ctx, p, action, res, "workspace"); err != nil {
		return ports.Workspace{}, ports.Project{}, err
	}
	if ws.StorageBackendID != s.cfg.StorageBackendID {
		return ports.Workspace{}, ports.Project{}, mapErr(ports.ErrStorageUnavailable, "workspace storage backend")
	}
	return ws, proj, nil
}

func (s *Service) check(ctx context.Context, p ports.Principal, action ports.Action, res ports.Resource, what string) error {
	switch err := s.d.Authz.Check(ctx, p, action, res); {
	case err == nil:
		return nil
	case errors.Is(err, ports.ErrForbidden):
		return &Error{Code: CodeForbidden, Message: "not allowed to " + string(action), Err: err}
	case errors.Is(err, ports.ErrNotFound):
		return &Error{Code: CodeNotFound, Message: what + " not found", Err: err}
	default:
		return err
	}
}

// snapshotAccess resolves a snapshot to its workspace and checks action.
func (s *Service) snapshotAccess(ctx context.Context, p ports.Principal, snapshotID string, action ports.Action) (ports.Snapshot, ports.Workspace, error) {
	if err := checkID("snapshot_id", snapshotID); err != nil {
		return ports.Snapshot{}, ports.Workspace{}, err
	}
	snap, err := s.d.Store.GetSnapshot(ctx, nil, snapshotID)
	if err != nil {
		return ports.Snapshot{}, ports.Workspace{}, mapErr(err, "snapshot")
	}
	ws, _, err := s.access(ctx, p, snap.WorkspaceID, action)
	if err != nil {
		if CodeOf(err) == CodeNotFound {
			err = notFound("snapshot")
		}
		return ports.Snapshot{}, ports.Workspace{}, err
	}
	return snap, ws, nil
}

// getter reads the tree nodes of a workspace.
func (s *Service) getter(workspaceID string) tree.Getter {
	return tree.GetterFunc(func(ctx context.Context, h contenthash.Hash) ([]byte, error) {
		return s.d.Trees.Get(ctx, workspaceID, h)
	})
}

// Page is the size and position of one page of a list.
type Page struct {
	Limit  int    // 1..MaxLimit; 0 = DefaultLimit
	Cursor string // the NextCursor of the previous page; "" = first page
}

func (pg Page) limit() (int, error) {
	switch {
	case pg.Limit == 0:
		return DefaultLimit, nil
	case pg.Limit < 1 || pg.Limit > MaxLimit:
		return 0, invalid(fmt.Sprintf("limit must be between 1 and %d", MaxLimit))
	}
	return pg.Limit, nil
}

// A cursor is scoped to the list kind, resource and normalized filter.
// Hash the scope so a long directory name does not inflate the next URL.
type listScope [sha256.Size]byte

func cursorScope(kind byte, resource, filter string) listScope {
	return sha256.Sum256([]byte(string(kind) + resource + "\x00" + filter))
}

func (scope listScope) encode(pos string) string {
	return base64.RawURLEncoding.EncodeToString(append(scope[:], pos...))
}

func (scope listScope) position(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(b) <= len(scope) || listScope(b[:len(scope)]) != scope {
		return "", invalidCursor()
	}
	return string(b[len(scope):]), nil
}

func invalidCursor() *Error { return invalidReason(ReasonInvalidCursor, "invalid cursor") }

const (
	cursorBranches = 'b'
	cursorTree     = 't'
	cursorDiff     = 'd'
	cursorTimeline = 's'
)

func (scope listScope) timelineCursor(c ports.TimelineCursor) string {
	return scope.encode(strconv.FormatInt(c.CreatedAtUs, 10) + ":" + c.SnapshotID)
}

func (scope listScope) parseTimelineCursor(c string) (*ports.TimelineCursor, error) {
	pos, err := scope.position(c)
	if err != nil {
		return nil, err
	}
	us, id, ok := strings.Cut(pos, ":")
	n, perr := strconv.ParseInt(us, 10, 64)
	if !ok || perr != nil || !isULID(id) {
		return nil, invalidCursor()
	}
	return &ports.TimelineCursor{CreatedAtUs: n, SnapshotID: id}, nil
}
