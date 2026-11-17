package authz_test

import (
	"context"
	"errors"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/authz"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

func TestProjectOwner(t *testing.T) {
	ctx := context.Background()
	z := authz.ProjectOwner{}
	alice, bob := ports.Principal{Ref: "alice"}, ports.Principal{Ref: "bob"}
	tenant := ports.Resource{Type: ports.ResourceTenant, ID: "T", TenantID: "T"}
	ws := ports.Resource{Type: ports.ResourceWorkspace, ID: "W", ProjectID: "P", WorkspaceID: "W", OwnerRef: "alice"}
	for _, c := range []struct {
		p    ports.Principal
		act  ports.Action
		res  ports.Resource
		want error
	}{
		{alice, ports.ActionProjectCreate, tenant, nil},
		{bob, ports.ActionProjectCreate, tenant, nil},
		{alice, ports.ActionWorkspaceWrite, ws, nil},
		{alice, ports.ActionWorkspaceRead, ws, nil},
		{bob, ports.ActionWorkspaceRead, ws, ports.ErrNotFound},
		{ports.Principal{Ref: "alice "}, ports.ActionWorkspaceRead, ws, ports.ErrNotFound}, // byte for byte
		{alice, ports.ActionWorkspaceRead, ports.Resource{Type: ports.ResourceWorkspace, ID: "X"}, ports.ErrNotFound},
		{ports.Principal{}, ports.ActionProjectCreate, tenant, ports.ErrNotFound},
		{alice, ports.ActionProjectCreate, ws, nil}, // owner of the resource
	} {
		if err := z.Check(ctx, c.p, c.act, c.res); !errors.Is(err, c.want) || c.want == nil && err != nil {
			t.Errorf("%+v %s %+v: %v, want %v", c.p, c.act, c.res, err, c.want)
		}
	}
}

func TestProjectScope(t *testing.T) {
	ctx := context.Background()
	z := authz.ProjectScope{}
	inP := ports.Principal{Ref: "manager", ActorType: "human", ProjectScope: "P"}
	tenant := ports.Resource{Type: ports.ResourceTenant, ID: "T", TenantID: "T"}
	wsP := ports.Resource{Type: ports.ResourceWorkspace, ID: "W", ProjectID: "P", WorkspaceID: "W", OwnerRef: "someone else"}
	wsQ := ports.Resource{Type: ports.ResourceWorkspace, ID: "W2", ProjectID: "Q", WorkspaceID: "W2", OwnerRef: "manager"}
	projP := ports.Resource{Type: ports.ResourceProject, ID: "P", ProjectID: "P"}
	for _, c := range []struct {
		p    ports.Principal
		act  ports.Action
		res  ports.Resource
		want error
	}{
		{inP, ports.ActionWorkspaceWrite, wsP, nil}, // the owner does not matter
		{inP, ports.ActionWorkspaceRead, wsP, nil},
		{inP, ports.ActionWorkspaceCreate, projP, nil},
		{inP, ports.ActionWorkspaceRead, wsQ, ports.ErrNotFound}, // another project, even as its owner
		{inP, ports.ActionProjectCreate, tenant, ports.ErrNotFound},
		{ports.Principal{Ref: "manager"}, ports.ActionWorkspaceRead, wsP, ports.ErrNotFound},    // no scope
		{ports.Principal{ProjectScope: "P"}, ports.ActionWorkspaceRead, wsP, ports.ErrNotFound}, // no actor
		{inP, ports.ActionWorkspaceRead, ports.Resource{Type: ports.ResourceWorkspace, ID: "X"}, ports.ErrNotFound},
	} {
		if err := z.Check(ctx, c.p, c.act, c.res); !errors.Is(err, c.want) || c.want == nil && err != nil {
			t.Errorf("%+v %s %+v: %v, want %v", c.p, c.act, c.res, err, c.want)
		}
	}
}
