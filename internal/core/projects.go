package core

import (
	"context"
	"errors"

	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/storagekey"
)

// CreateProjectRequest is the body of POST /projects.
type CreateProjectRequest struct {
	Name string `json:"name"`
}

// errNoProjects answers the project operations when the host platform owns
// the projects: the same not_found as for an unknown endpoint.
func errNoProjects() *Error { return newError(CodeNotFound, "no such endpoint", nil) }

// CreateProject creates a project owned by the caller (standalone
// deployments: the creator is its only owner) in the configured tenant.
func (s *Service) CreateProject(ctx context.Context, p ports.Principal, req CreateProjectRequest, idemKey string) (ports.Project, error) {
	if s.cfg.HostOwnsProjects {
		return ports.Project{}, errNoProjects()
	}
	if err := checkLabel("name", req.Name, true); err != nil {
		return ports.Project{}, err
	}
	res := ports.Resource{Type: ports.ResourceTenant, ID: s.cfg.TenantID, TenantID: s.cfg.TenantID}
	if err := s.check(ctx, p, ports.ActionProjectCreate, res, "tenant"); err != nil {
		return ports.Project{}, err
	}
	return idempotent(ctx, s, p, RouteCreateProject, idemKey, req, func(ctx context.Context, complete completer) (ports.Project, error) {
		now := s.nowUs()
		proj := ports.Project{ID: s.d.IDs.NewID(), TenantID: s.cfg.TenantID, Name: req.Name, Status: ports.StatusActive,
			OwnerRef: p.Ref, OwnerPrincipalUID: p.PrincipalUID, CreatedAtUs: now, UpdatedAtUs: now}
		err := s.d.Store.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
			if err := s.d.Store.InsertProject(ctx, tx, proj); err != nil {
				return err
			}
			return completeIf(ctx, complete, tx, proj)
		})
		return proj, mapErr(err, "project")
	})
}

// GetProject returns a project the caller can see.
func (s *Service) GetProject(ctx context.Context, p ports.Principal, id string) (ports.Project, error) {
	if s.cfg.HostOwnsProjects {
		return ports.Project{}, errNoProjects()
	}
	if err := checkID("project_id", id); err != nil {
		return ports.Project{}, err
	}
	proj, err := s.d.Store.GetProject(ctx, nil, id)
	if err != nil {
		return ports.Project{}, mapErr(err, "project")
	}
	if err := s.checkProject(ctx, p, ports.ActionProjectRead, proj); err != nil {
		return ports.Project{}, err
	}
	return proj, nil
}

func (s *Service) checkProject(ctx context.Context, p ports.Principal, action ports.Action, proj ports.Project) error {
	if proj.TenantID != s.cfg.TenantID {
		return notFound("project")
	}
	res := ports.Resource{Type: ports.ResourceProject, ID: proj.ID, TenantID: proj.TenantID, ProjectID: proj.ID, OwnerRef: proj.OwnerRef}
	return s.check(ctx, p, action, res, "project")
}

// CreateWorkspaceRequest is the body of POST /workspaces.
type CreateWorkspaceRequest struct {
	ProjectID   string `json:"project_id"`
	DisplayName string `json:"display_name"`
}

// WorkspaceView is a workspace with the ID of its main branch.
type WorkspaceView struct {
	Workspace    ports.Workspace
	MainBranchID string
}

// CreateWorkspace creates a project-level workspace and, in the same
// transaction, its main branch (ref main, generation 1, no head), its
// usage row and the events mfgraph.workspace_created and
// mfgraph.branch_created. The storage prefix is the default prefix
// tenants/<tenant>/projects/<project>/workspaces/<workspace>: default
// prefixes have a fixed number of segments, so they are never nested, and
// the database rejects two that differ only in case.
func (s *Service) CreateWorkspace(ctx context.Context, p ports.Principal, req CreateWorkspaceRequest, idemKey string) (WorkspaceView, error) {
	if err := checkID("project_id", req.ProjectID); err != nil {
		return WorkspaceView{}, err
	}
	if err := checkLabel("display_name", req.DisplayName, false); err != nil {
		return WorkspaceView{}, err
	}
	// When the host owns the projects, the project is the one of the
	// request as given: the authorizer admits only the request's project,
	// whose existence is the host's business.
	proj := ports.Project{ID: req.ProjectID, TenantID: s.cfg.TenantID}
	if !s.cfg.HostOwnsProjects {
		var err error
		if proj, err = s.d.Store.GetProject(ctx, nil, req.ProjectID); err != nil {
			return WorkspaceView{}, mapErr(err, "project")
		}
	}
	if err := s.checkProject(ctx, p, ports.ActionWorkspaceCreate, proj); err != nil {
		return WorkspaceView{}, err
	}
	return idempotent(ctx, s, p, RouteCreateWorkspace, idemKey, req, func(ctx context.Context, complete completer) (WorkspaceView, error) {
		now := s.nowUs()
		id := s.d.IDs.NewID()
		prefix, err := storagekey.DefaultPrefix(proj.TenantID, proj.ID, id)
		if err != nil {
			return WorkspaceView{}, err
		}
		ws := ports.Workspace{ID: id, TenantID: proj.TenantID, ProjectID: proj.ID, Type: ports.WorkspaceTypeProject,
			OwnerRef: p.Ref, OwnerPrincipalUID: p.PrincipalUID, OwnerType: p.ActorType, StorageBackendID: s.cfg.StorageBackendID,
			StoragePrefix: prefix, StoragePrefixKey: storagekey.PrefixKey(prefix), DisplayName: req.DisplayName,
			Status: ports.StatusActive, CreatedAtUs: now, UpdatedAtUs: now}
		main := ports.Branch{ID: s.d.IDs.NewID(), WorkspaceID: id, ProjectID: proj.ID, Name: ports.MainRefName,
			RefName: ports.MainRefName, Type: ports.BranchTypeMain, Generation: 1, Status: ports.StatusActive,
			CreatedByRef: p.Ref, CreatedByPrincipalUID: p.PrincipalUID, CreatedAtUs: now, UpdatedAtUs: now}
		view := WorkspaceView{Workspace: ws, MainBranchID: main.ID}
		evs, err := s.events(p, proj.ID, now,
			eventSpec{kind: ports.EventWorkspaceCreated, workspaceID: id, key: id, version: 1,
				data: workspaceCreatedData{WorkspaceID: id, MainBranchID: main.ID, StorageBackendID: ws.StorageBackendID}},
			eventSpec{kind: ports.EventBranchCreated, workspaceID: id, key: main.ID, version: main.Generation,
				data: branchCreatedData{WorkspaceID: id, BranchID: main.ID, RefName: main.RefName, Generation: main.Generation}})
		if err != nil {
			return WorkspaceView{}, err
		}
		err = s.d.Store.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
			if err := s.d.Store.InsertWorkspace(ctx, tx, ws); err != nil {
				return err
			}
			if err := s.d.Store.InsertBranch(ctx, tx, main); err != nil {
				return err
			}
			if err := s.d.Store.InsertUsage(ctx, tx, id, now); err != nil {
				return err
			}
			if err := s.appendEvents(ctx, tx, evs); err != nil {
				return err
			}
			return completeIf(ctx, complete, tx, view)
		})
		if errors.Is(err, ports.ErrAlreadyExists) {
			return WorkspaceView{}, &Error{Code: CodeConflict, Message: "the storage prefix of the new workspace is in use", Err: err}
		}
		return view, mapErr(err, "workspace")
	})
}

// GetWorkspace returns a workspace the caller can see.
func (s *Service) GetWorkspace(ctx context.Context, p ports.Principal, id string) (WorkspaceView, error) {
	if err := checkID("workspace_id", id); err != nil {
		return WorkspaceView{}, err
	}
	ws, _, err := s.access(ctx, p, id, ports.ActionWorkspaceRead)
	if err != nil {
		return WorkspaceView{}, err
	}
	main, err := s.d.Store.GetBranchByRef(ctx, nil, id, ports.MainRefName)
	if err != nil {
		return WorkspaceView{}, mapErr(err, "main branch")
	}
	return WorkspaceView{Workspace: ws, MainBranchID: main.ID}, nil
}

// ListBranches lists the branches of a workspace by ref name.
func (s *Service) ListBranches(ctx context.Context, p ports.Principal, workspaceID string, pg Page) ([]ports.Branch, string, error) {
	if err := checkID("workspace_id", workspaceID); err != nil {
		return nil, "", err
	}
	limit, err := pg.limit()
	if err != nil {
		return nil, "", err
	}
	after := ""
	if pg.Cursor != "" {
		if after, err = decodeCursor(cursorBranches, pg.Cursor); err != nil {
			return nil, "", err
		}
	}
	if _, _, err := s.access(ctx, p, workspaceID, ports.ActionWorkspaceRead); err != nil {
		return nil, "", err
	}
	bs, err := s.d.Store.ListBranches(ctx, nil, workspaceID, after, limit+1)
	if err != nil {
		return nil, "", mapErr(err, "branches")
	}
	next := ""
	if len(bs) > limit {
		bs = bs[:limit]
		next = encodeCursor(cursorBranches, bs[limit-1].RefName)
	}
	return bs, next, nil
}

// branchOf returns a branch of the workspace (not_found for a branch of
// another workspace).
func (s *Service) branchOf(ctx context.Context, workspaceID, branchID string) (ports.Branch, error) {
	if err := checkID("branch_id", branchID); err != nil {
		return ports.Branch{}, err
	}
	b, err := s.d.Store.GetBranch(ctx, nil, branchID)
	if err != nil {
		return ports.Branch{}, mapErr(err, "branch")
	}
	if b.WorkspaceID != workspaceID {
		return ports.Branch{}, notFound("branch")
	}
	return b, nil
}
