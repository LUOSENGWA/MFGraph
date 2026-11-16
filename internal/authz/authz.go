// Package authz holds the authorization policy of standalone deployments.
//
// ProjectOwner: any authenticated principal may create a project; the
// principal that created a project (its owner_ref, compared byte for byte
// with the caller's actor reference) is the only one that can see it and
// its workspaces and do anything there. Everyone else gets ErrNotFound, so
// that the existence of other projects is not revealed. Roles (reader,
// editor, reviewer, owner) come later; embedded in a host platform, the
// host's access control decides instead.
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
