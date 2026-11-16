package api

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/api/gen"
	"github.com/LUOSENGWA/MFGraph/internal/core"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/tree"
)

// service returns the core service, or db_unavailable when the server has
// no database.
func (h *handlers) service(ctx context.Context) (*core.Service, ports.Principal, error) {
	if h.svc == nil {
		return nil, ports.Principal{}, NewError(CodeDBUnavailable, "no database is configured", map[string]any{"reason": "not_configured"})
	}
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return nil, ports.Principal{}, NewError(CodeUnauthenticated, "missing or invalid bearer token", nil)
	}
	return h.svc, p, nil
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func page(limit *int32, cursor *string) core.Page {
	pg := core.Page{Cursor: str(cursor)}
	if limit != nil {
		pg.Limit = int(*limit)
		if pg.Limit == 0 {
			pg.Limit = -1 // an explicit 0 is out of range, not the default
		}
	}
	return pg
}

func next(c string) *string {
	if c == "" {
		return nil
	}
	return &c
}

func ts(us int64) time.Time { return time.UnixMicro(us).UTC() }

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ---- conversions --------------------------------------------------------------

func projectJSON(p ports.Project) gen.Project {
	return gen.Project{ProjectId: p.ID, TenantId: p.TenantID, Name: p.Name, Status: p.Status,
		CreatedAt: ts(p.CreatedAtUs), UpdatedAt: ts(p.UpdatedAtUs)}
}

func workspaceJSON(v core.WorkspaceView) gen.Workspace {
	w := v.Workspace
	return gen.Workspace{WorkspaceId: w.ID, ProjectId: w.ProjectID, TenantId: w.TenantID, WorkspaceType: w.Type,
		DisplayName: w.DisplayName, Status: w.Status, MainBranchId: v.MainBranchID, CreatedAt: ts(w.CreatedAtUs),
		UpdatedAt: ts(w.UpdatedAtUs)}
}

func branchJSON(b ports.Branch) gen.Branch {
	return gen.Branch{BranchId: b.ID, WorkspaceId: b.WorkspaceID, BranchName: b.Name, RefName: b.RefName, BranchType: b.Type,
		HeadSnapshotId: nullable(b.HeadSnapshotID), Generation: b.Generation, Status: b.Status, CreatedAt: ts(b.CreatedAtUs),
		UpdatedAt: ts(b.UpdatedAtUs)}
}

func snapshotJSON(s ports.Snapshot) gen.Snapshot {
	return gen.Snapshot{SnapshotId: s.ID, WorkspaceId: s.WorkspaceID, ProjectId: s.ProjectID, BranchId: nullable(s.BranchID),
		RefName: s.RefName, AuthorRef: s.AuthorRef, AuthorPrincipalUid: nullable(s.AuthorPrincipalUID), Message: s.Message,
		TriggerKind: s.TriggerKind, RootTreeHash: s.RootTreeHash.String(), TreeFormatVersion: int32(s.TreeFormatVersion),
		ParentIds: append([]string{}, s.ParentIDs...), EntryCount: s.EntryCount, TotalBytes: s.TotalBytes, CreatedAt: ts(s.CreatedAtUs)}
}

func commitJSON(r core.CommitResult) gen.CommitResult {
	return gen.CommitResult{SnapshotId: r.SnapshotID, BranchId: r.BranchID, RefName: r.RefName, Generation: r.Generation,
		RootTreeHash: r.RootTreeHash, ParentIds: append([]string{}, r.ParentIDs...), RestoredFrom: nullable(r.RestoredFrom),
		CreatedAt: ts(r.CreatedAtUs)}
}

func fileVersion(e *tree.Entry) *gen.FileVersion {
	if e == nil {
		return nil
	}
	return &gen.FileVersion{Hash: e.Hash.String(), Size: e.Size, Mode: gen.FileMode(e.Mode)}
}

// ---- projects and workspaces --------------------------------------------------

func (h *handlers) CreateProject(ctx context.Context, req gen.CreateProjectRequestObject) (gen.CreateProjectResponseObject, error) {
	svc, p, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	proj, err := svc.CreateProject(ctx, p, core.CreateProjectRequest{Name: req.Body.Name}, str(req.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	return gen.CreateProject201JSONResponse(projectJSON(proj)), nil
}

func (h *handlers) GetProject(ctx context.Context, req gen.GetProjectRequestObject) (gen.GetProjectResponseObject, error) {
	svc, p, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	proj, err := svc.GetProject(ctx, p, req.ProjectId)
	if err != nil {
		return nil, err
	}
	return gen.GetProject200JSONResponse(projectJSON(proj)), nil
}

func (h *handlers) CreateWorkspace(ctx context.Context, req gen.CreateWorkspaceRequestObject) (gen.CreateWorkspaceResponseObject, error) {
	svc, p, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	v, err := svc.CreateWorkspace(ctx, p, core.CreateWorkspaceRequest{ProjectID: req.Body.ProjectId,
		DisplayName: str(req.Body.DisplayName)}, str(req.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	return gen.CreateWorkspace201JSONResponse(workspaceJSON(v)), nil
}

func (h *handlers) GetWorkspace(ctx context.Context, req gen.GetWorkspaceRequestObject) (gen.GetWorkspaceResponseObject, error) {
	svc, p, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	v, err := svc.GetWorkspace(ctx, p, req.WorkspaceId)
	if err != nil {
		return nil, err
	}
	return gen.GetWorkspace200JSONResponse(workspaceJSON(v)), nil
}

func (h *handlers) ListBranches(ctx context.Context, req gen.ListBranchesRequestObject) (gen.ListBranchesResponseObject, error) {
	svc, p, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	bs, nc, err := svc.ListBranches(ctx, p, req.WorkspaceId, page(req.Params.Limit, req.Params.Cursor))
	if err != nil {
		return nil, err
	}
	out := gen.BranchList{Items: make([]gen.Branch, len(bs)), NextCursor: next(nc)}
	for i, b := range bs {
		out.Items[i] = branchJSON(b)
	}
	return gen.ListBranches200JSONResponse(out), nil
}

// ---- objects, commits, restores -----------------------------------------------

func (h *handlers) PutObject(ctx context.Context, req gen.PutObjectRequestObject) (gen.PutObjectResponseObject, error) {
	svc, p, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	b, created, err := svc.PutObject(ctx, p, core.PutObjectRequest{WorkspaceID: req.WorkspaceId, Body: req.Body,
		ContentLength: ContentLengthFrom(ctx), Expected: str(req.Params.XContentSHA256)})
	if err != nil {
		return nil, err
	}
	o := gen.StagedObject{Hash: b.Hash.String(), Size: b.Size, Status: gen.StagedObjectStatus(b.Status)}
	if created {
		return gen.PutObject201JSONResponse(o), nil
	}
	return gen.PutObject200JSONResponse(o), nil
}

func (h *handlers) CreateCommit(ctx context.Context, req gen.CreateCommitRequestObject) (gen.CreateCommitResponseObject, error) {
	svc, p, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	cr := core.CommitRequest{BranchID: req.Body.BranchId, ExpectedGeneration: req.Body.ExpectedGeneration,
		Message: str(req.Body.Message), Changes: make([]core.Change, len(req.Body.Changes))}
	for i, c := range req.Body.Changes {
		cc := core.Change{Path: c.Path, Hash: c.Hash, Delete: c.Delete}
		if c.Mode != nil {
			m := int(*c.Mode)
			cc.Mode = &m
		}
		cr.Changes[i] = cc
	}
	res, err := svc.Commit(ctx, p, req.WorkspaceId, cr, str(req.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	return gen.CreateCommit201JSONResponse(commitJSON(res)), nil
}

func (h *handlers) RestoreSnapshot(ctx context.Context, req gen.RestoreSnapshotRequestObject) (gen.RestoreSnapshotResponseObject, error) {
	svc, p, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	rr := core.RestoreRequest{BranchID: req.Body.BranchId, ExpectedGeneration: req.Body.ExpectedGeneration,
		Message: str(req.Body.Message), Path: req.Body.Path}
	res, err := svc.Restore(ctx, p, req.SnapshotId, rr, str(req.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	return gen.RestoreSnapshot201JSONResponse(commitJSON(res)), nil
}

// ---- reads --------------------------------------------------------------------

func (h *handlers) GetTimeline(ctx context.Context, req gen.GetTimelineRequestObject) (gen.GetTimelineResponseObject, error) {
	svc, p, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	snaps, nc, err := svc.Timeline(ctx, p, req.WorkspaceId, str(req.Params.BranchId), page(req.Params.Limit, req.Params.Cursor))
	if err != nil {
		return nil, err
	}
	out := gen.SnapshotList{Items: make([]gen.Snapshot, len(snaps)), NextCursor: next(nc)}
	for i, s := range snaps {
		out.Items[i] = snapshotJSON(s)
	}
	return gen.GetTimeline200JSONResponse(out), nil
}

func (h *handlers) GetSnapshot(ctx context.Context, req gen.GetSnapshotRequestObject) (gen.GetSnapshotResponseObject, error) {
	svc, p, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	s, err := svc.GetSnapshot(ctx, p, req.SnapshotId)
	if err != nil {
		return nil, err
	}
	return gen.GetSnapshot200JSONResponse(snapshotJSON(s)), nil
}

func (h *handlers) ListSnapshotTree(ctx context.Context, req gen.ListSnapshotTreeRequestObject) (gen.ListSnapshotTreeResponseObject, error) {
	svc, p, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	l, err := svc.ListTree(ctx, p, req.SnapshotId, str(req.Params.Path), page(req.Params.Limit, req.Params.Cursor))
	if err != nil {
		return nil, err
	}
	out := gen.TreeListing{SnapshotId: l.SnapshotID, Path: l.Path, Items: make([]gen.TreeEntry, len(l.Items)), NextCursor: next(l.NextCursor)}
	for i, x := range l.Items {
		e := gen.TreeEntry{Name: x.Entry.Name, Path: x.Path, Type: gen.TreeEntryType(x.Entry.Type), Hash: x.Entry.Hash.String()}
		if x.Entry.Type == tree.TypeFile {
			size, mode := x.Entry.Size, gen.FileMode(x.Entry.Mode)
			e.Size, e.Mode = &size, &mode
		}
		out.Items[i] = e
	}
	return gen.ListSnapshotTree200JSONResponse(out), nil
}

func (h *handlers) GetSnapshotDiff(ctx context.Context, req gen.GetSnapshotDiffRequestObject) (gen.GetSnapshotDiffResponseObject, error) {
	svc, p, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	d, err := svc.Diff(ctx, p, req.SnapshotId, str(req.Params.Base), page(req.Params.Limit, req.Params.Cursor))
	if err != nil {
		return nil, err
	}
	out := gen.Diff{BaseSnapshotId: nullable(d.BaseSnapshotID), TargetSnapshotId: d.TargetSnapshotID,
		Items: make([]gen.DiffEntry, len(d.Items)), NextCursor: next(d.NextCursor)}
	for i, x := range d.Items {
		out.Items[i] = gen.DiffEntry{Path: x.Path, Change: gen.DiffEntryChange(x.Kind), Old: fileVersion(x.Old), New: fileVersion(x.New)}
	}
	return gen.GetSnapshotDiff200JSONResponse(out), nil
}

func (h *handlers) GetSnapshotFile(ctx context.Context, req gen.GetSnapshotFileRequestObject) (gen.GetSnapshotFileResponseObject, error) {
	svc, p, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	f, err := svc.GetFile(ctx, p, req.SnapshotId, req.Params.Path)
	if err != nil {
		return nil, err
	}
	etag, ranges, nosniff, disposition := f.ETag(), "bytes", "nosniff", contentDisposition(f.Path)
	if inm := req.Params.IfNoneMatch; inm != nil && etagMatches(*inm, etag) {
		return gen.GetSnapshotFile304Response{Headers: gen.GetSnapshotFile304ResponseHeaders{ETag: &etag}}, nil
	}
	size := f.Entry.Size
	if req.Params.Range != nil {
		start, end, ok, satisfiable := parseRange(*req.Params.Range, size)
		if ok && !satisfiable {
			cr, id := fmt.Sprintf("bytes */%d", size), RequestIDFrom(ctx)
			return gen.GetSnapshotFile416JSONResponse{Headers: gen.GetSnapshotFile416ResponseHeaders{ContentRange: &cr},
				Body: gen.Error{Code: CodeInvalidRequest, Message: "range not satisfiable", RequestId: &id}}, nil
		}
		if ok {
			body, err := f.Open(ctx, start, end-start+1)
			if err != nil {
				return nil, err
			}
			cr := fmt.Sprintf("bytes %d-%d/%d", start, end, size)
			return gen.GetSnapshotFile206ApplicationoctetStreamResponse{Body: body, ContentLength: end - start + 1,
				Headers: gen.GetSnapshotFile206ResponseHeaders{ETag: &etag, AcceptRanges: &ranges, ContentRange: &cr,
					ContentDisposition: &disposition, XContentTypeOptions: &nosniff}}, nil
		}
	}
	body, err := f.Open(ctx, 0, -1)
	if err != nil {
		return nil, err
	}
	return gen.GetSnapshotFile200ApplicationoctetStreamResponse{Body: body, ContentLength: size,
		Headers: gen.GetSnapshotFile200ResponseHeaders{ETag: &etag, AcceptRanges: &ranges,
			ContentDisposition: &disposition, XContentTypeOptions: &nosniff}}, nil
}

// contentDisposition is the Content-Disposition of a download (RFC 6266):
// attachment, with the name of the file (the last segment of its stored
// path) in UTF-8 as filename* (RFC 8187 percent-encoding) and an ASCII
// fallback as filename, in which every other character, '"', '\' and '%'
// is replaced by '_'.
func contentDisposition(path string) string {
	name := path[strings.LastIndexByte(path, '/')+1:]
	var fallback, ext strings.Builder
	for _, r := range name {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' || r == '%' {
			fallback.WriteByte('_')
		} else {
			fallback.WriteRune(r)
		}
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; isAttrChar(c) {
			ext.WriteByte(c)
		} else {
			fmt.Fprintf(&ext, "%%%02X", c)
		}
	}
	return `attachment; filename="` + fallback.String() + `"; filename*=UTF-8''` + ext.String()
}

// isAttrChar reports the characters RFC 8187 leaves unencoded (attr-char).
func isAttrChar(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("!#$&+-.^_`|~", c) >= 0
}

// etagMatches implements the weak comparison of If-None-Match: "*" or a
// list of entity tags, each possibly marked weak (W/).
func etagMatches(header, etag string) bool {
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimSpace(t)
		if t == "*" || strings.TrimPrefix(t, "W/") == etag {
			return true
		}
	}
	return false
}

// parseRange parses a Range header with one byte range of a file of size
// bytes. ok is false for a header the server ignores (another unit,
// several ranges, invalid syntax); satisfiable is false when the range
// lies outside the file (416).
func parseRange(h string, size int64) (start, end int64, ok, satisfiable bool) {
	spec, found := strings.CutPrefix(strings.TrimSpace(h), "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, 0, false, false
	}
	first, last, found := strings.Cut(strings.TrimSpace(spec), "-")
	if !found {
		return 0, 0, false, false
	}
	first, last = strings.TrimSpace(first), strings.TrimSpace(last)
	num := func(s string) (int64, bool) {
		if s == "" || strings.TrimLeft(s, "0123456789") != "" {
			return 0, false
		}
		n, err := strconv.ParseInt(s, 10, 64)
		return n, err == nil
	}
	switch {
	case first == "": // the last n bytes
		n, valid := num(last)
		if !valid {
			return 0, 0, false, false
		}
		if n == 0 || size == 0 {
			return 0, 0, true, false
		}
		return max(size-n, 0), size - 1, true, true
	default:
		a, valid := num(first)
		if !valid {
			return 0, 0, false, false
		}
		b := size - 1
		if last != "" {
			if b, valid = num(last); !valid || b < a {
				return 0, 0, false, false
			}
		}
		if a >= size {
			return 0, 0, true, false
		}
		return a, min(b, size-1), true, true
	}
}
