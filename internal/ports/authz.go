package ports

import "context"

// Action is an operation checked by the Authorizer.
type Action string

// Actions of the first core slice.
const (
	ActionProjectCreate   Action = "project:create"
	ActionProjectRead     Action = "project:read"
	ActionWorkspaceCreate Action = "workspace:create"
	ActionWorkspaceRead   Action = "workspace:read"  // read metadata, trees, files, diffs
	ActionWorkspaceWrite  Action = "workspace:write" // upload, commit, restore
)

// ResourceType names the kind of a Resource.
type ResourceType string

const (
	ResourceTenant    ResourceType = "tenant"
	ResourceProject   ResourceType = "project"
	ResourceWorkspace ResourceType = "workspace"
	ResourceBranch    ResourceType = "branch"
	ResourceSnapshot  ResourceType = "snapshot"
)

// Resource identifies what an action applies to. Every ID is resolved to its
// owning workspace (and project and tenant) before Check is called; the
// owner fields are what authorization decides on.
type Resource struct {
	Type        ResourceType
	ID          string
	TenantID    string
	ProjectID   string
	WorkspaceID string // empty for tenant- and project-level resources
}

// Authorizer decides whether a principal may perform an action. Principals
// are identified by their actor reference (Principal.Ref, compared byte for
// byte). It returns nil, ErrForbidden (the principal can see the resource but
// not do this) or ErrNotFound (the principal cannot see the resource;
// existence is hidden, which is also what callers answer for resources of
// other workspaces).
type Authorizer interface {
	Check(ctx context.Context, p Principal, action Action, res Resource) error
}
