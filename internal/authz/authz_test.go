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
