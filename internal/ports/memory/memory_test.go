package memory_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/ids"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/ports/memory"
	"github.com/LUOSENGWA/MFGraph/internal/ports/porttest"
)

var ctx = context.Background()

func TestBlobStoreContract(t *testing.T) {
	porttest.RunBlobStore(t, func(*testing.T) (ports.BlobStore, porttest.Corrupter) {
		s := memory.NewBlobStore()
		return s, s
	})
}

func TestAuthenticatorAndAuthorizer(t *testing.T) {
	a := &memory.Authenticator{Tokens: map[string]ports.Principal{"tok": {Ref: "U1", TenantID: "T"}}}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer tok")
	if p, err := a.Authenticate(r); err != nil || p.Ref != "U1" {
		t.Fatalf("Authenticate = %+v %v", p, err)
	}
	r.Header.Set("Authorization", "Bearer other")
	if _, err := a.Authenticate(r); !errors.Is(err, ports.ErrUnauthenticated) {
		t.Fatalf("bad token: %v", err)
	}

	z := &memory.Authorizer{}
	z.Allow(memory.Grant{ActorRef: "U1", WorkspaceID: "WA", Actions: []ports.Action{ports.ActionWorkspaceRead}})
	z.Allow(memory.Grant{ActorRef: "U2", ProjectID: "P", Actions: []ports.Action{ports.ActionWorkspaceRead, ports.ActionWorkspaceWrite}})
	u1, u2 := ports.Principal{Ref: "U1"}, ports.Principal{Ref: "U2"}
	wa := ports.Resource{Type: ports.ResourceSnapshot, ID: "S1", ProjectID: "P", WorkspaceID: "WA"}
	wb := ports.Resource{Type: ports.ResourceSnapshot, ID: "S2", ProjectID: "P", WorkspaceID: "WB"}
	cases := []struct {
		p    ports.Principal
		act  ports.Action
		res  ports.Resource
		want error
	}{
		{u1, ports.ActionWorkspaceRead, wa, nil},
		{u1, ports.ActionWorkspaceWrite, wa, ports.ErrForbidden}, // visible, not allowed
		{u1, ports.ActionWorkspaceRead, wb, ports.ErrNotFound},   // other workspace: hidden
		{u2, ports.ActionWorkspaceWrite, wb, nil},                // project-wide grant
		{ports.Principal{Ref: "U3"}, ports.ActionWorkspaceRead, wa, ports.ErrNotFound},
		{ports.Principal{Ref: "U1 "}, ports.ActionWorkspaceRead, wa, ports.ErrNotFound}, // byte for byte
	}
	for i, c := range cases {
		if err := z.Check(ctx, c.p, c.act, c.res); !errors.Is(err, c.want) || (c.want == nil && err != nil) {
			t.Errorf("case %d: got %v, want %v", i, err, c.want)
		}
	}
}

func TestClockAndIDGen(t *testing.T) {
	start := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	c := memory.NewClock(start)
	c.Advance(time.Second)
	if !c.Now().Equal(start.Add(time.Second)) {
		t.Fatal("Advance")
	}
	g := memory.NewIDGen(c)
	a, b := g.NewID(), g.NewID()
	if !ids.Valid(a) || !ids.Valid(b) || !(a < b) {
		t.Fatalf("ids %s %s", a, b)
	}
	g2 := memory.NewIDGen(memory.NewClock(start.Add(time.Second)))
	if g2.NewID() != a {
		t.Fatal("IDGen is not deterministic")
	}
}

func TestOutboxTransactions(t *testing.T) {
	o := &memory.Outbox{}
	ev := func(id string) ports.Event { return ports.Event{ID: id, Type: ports.EventSnapshotCreated} }
	err := o.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		if tx.TxKind() != "memory" {
			t.Fatal("TxKind")
		}
		return o.Append(ctx, tx, ev("E1"))
	})
	if err != nil || len(o.Events()) != 1 {
		t.Fatalf("commit: %v %v", err, o.Events())
	}
	boom := errors.New("boom")
	err = o.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := o.Append(ctx, tx, ev("E2")); err != nil {
			return err
		}
		return boom // rollback: E2 must not become visible
	})
	if !errors.Is(err, boom) || len(o.Events()) != 1 {
		t.Fatalf("rollback: %v %v", err, o.Events())
	}
	var leaked ports.Tx
	_ = o.WithTx(ctx, func(_ context.Context, tx ports.Tx) error { leaked = tx; return nil })
	if err := o.Append(ctx, leaked, ev("E3")); !errors.Is(err, memory.ErrForeignTx) {
		t.Fatalf("finished tx: %v", err)
	}
	other := &memory.Outbox{}
	_ = other.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := o.Append(ctx, tx, ev("E4")); !errors.Is(err, memory.ErrForeignTx) {
			t.Errorf("foreign tx: %v", err)
		}
		return nil
	})
	_ = o.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := o.Append(ctx, tx, ports.Event{}); err == nil {
			t.Error("event without ID accepted")
		}
		return nil
	})
}
