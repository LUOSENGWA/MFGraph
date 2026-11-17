// Package authz holds the authorization policies.
//
// ProjectOwner (standalone deployments): any authenticated principal may
// create a project; the principal that created a project (its owner_ref,
// compared byte for byte with the caller's actor reference) is the only one
// that can see it and its workspaces and do anything there. Everyone else
// gets ErrNotFound, so that the existence of other projects is not
// revealed. Roles (reader, editor, reviewer, owner) come later.
//
// ProjectScope (module mode): the host platform's access control has
// admitted the request to one project (Principal.ProjectScope); MFGraph
// only checks that the resource belongs to that project.
package authz

import (
	"context"

	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

// ProjectOwner is the ports.Authorizer of standalone deployments.
type ProjectOwner struct{}

var _ ports.Authorizer = ProjectOwner{}

// Check implements ports.Authorizer.
func (ProjectOwner) Check(_ context.Context, p ports.Principal, action ports.Action, res ports.Resource) error {
	if p.Ref == "" {
		return ports.ErrNotFound
	}
	if action == ports.ActionProjectCreate && res.Type == ports.ResourceTenant {
		return nil
	}
	if res.OwnerRef != "" && res.OwnerRef == p.Ref {
		return nil
	}
	return ports.ErrNotFound
}

// ProjectScope is the ports.Authorizer of module mode: a resource is
// visible, with every action, exactly when it belongs to the project the
// host platform admitted the request to (Resource.ProjectID equals
// Principal.ProjectScope). Anything else, including creating a project, is
// ErrNotFound, the same answer as for a resource that does not exist. It
// never returns ErrForbidden: whether a caller may see a project but not
// change it is the host's decision, made before the request arrives.
type ProjectScope struct{}

var _ ports.Authorizer = ProjectScope{}

// Check implements ports.Authorizer.
func (ProjectScope) Check(_ context.Context, p ports.Principal, _ ports.Action, res ports.Resource) error {
	if p.Ref == "" || p.ProjectScope == "" || res.Type == ports.ResourceTenant || res.ProjectID != p.ProjectScope {
		return ports.ErrNotFound
	}
	return nil
}
