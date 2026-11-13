package memory

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

// Authenticator maps bearer tokens to principals.
type Authenticator struct {
	Tokens map[string]ports.Principal
}

var _ ports.Authenticator = (*Authenticator)(nil)

// Authenticate implements ports.Authenticator.
func (a *Authenticator) Authenticate(r *http.Request) (ports.Principal, error) {
	scheme, tok, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ports.Principal{}, ports.ErrUnauthenticated
	}
	p, ok := a.Tokens[strings.TrimSpace(tok)]
	if !ok {
		return ports.Principal{}, ports.ErrUnauthenticated
	}
	return p, nil
}

// Grant gives a principal actions on a scope: one workspace, all workspaces
// of a project, or a whole tenant (the most specific non-empty field wins).
type Grant struct {
	PrincipalUID string
	TenantID     string
	ProjectID    string
	WorkspaceID  string
	Actions      []ports.Action
}

func (g Grant) covers(res ports.Resource) bool {
	switch {
	case g.WorkspaceID != "":
		return res.WorkspaceID == g.WorkspaceID
	case g.ProjectID != "":
		return res.ProjectID == g.ProjectID
	case g.TenantID != "":
		return res.TenantID == g.TenantID
	}
	return false
}

// Authorizer checks grants. A principal with any grant covering the
// resource can see it (ErrForbidden for other actions); without one the
// resource is hidden (ErrNotFound).
type Authorizer struct {
	mu     sync.RWMutex
	grants []Grant
}

var _ ports.Authorizer = (*Authorizer)(nil)

// Allow adds a grant.
func (a *Authorizer) Allow(g Grant) {
	a.mu.Lock()
	a.grants = append(a.grants, g)
	a.mu.Unlock()
}

// Check implements ports.Authorizer.
func (a *Authorizer) Check(_ context.Context, p ports.Principal, action ports.Action, res ports.Resource) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	visible := false
	for _, g := range a.grants {
		if g.PrincipalUID != p.UID || !g.covers(res) {
			continue
		}
		visible = true
		for _, act := range g.Actions {
			if act == action {
				return nil
			}
		}
	}
	if visible {
		return ports.ErrForbidden
	}
	return ports.ErrNotFound
}

// Clock is a manually advanced clock, safe for concurrent use.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

var _ ports.Clock = (*Clock)(nil)

// NewClock returns a clock set to t.
func NewClock(t time.Time) *Clock { return &Clock{now: t} }

// Now implements ports.Clock.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// IDGen produces deterministic, valid, increasing ULIDs: the timestamp of
// the clock and a counter as entropy.
type IDGen struct {
	mu    sync.Mutex
	clock ports.Clock
	n     uint64
}

var _ ports.IDGen = (*IDGen)(nil)

// NewIDGen returns a generator using clock for the timestamp part.
func NewIDGen(clock ports.Clock) *IDGen { return &IDGen{clock: clock} }

// NewID implements ports.IDGen.
func (g *IDGen) NewID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	var id ulid.ULID
	if err := id.SetTime(ulid.Timestamp(g.clock.Now())); err != nil {
		panic(err)
	}
	var entropy [10]byte
	binary.BigEndian.PutUint64(entropy[2:], g.n)
	if err := id.SetEntropy(entropy[:]); err != nil {
		panic(err)
	}
	return id.String()
}

// Outbox is an in-memory ports.Transactor and ports.EventSink: events
// appended in a transaction become visible only when it commits.
type Outbox struct {
	mu        sync.Mutex
	committed []ports.Event
}

var (
	_ ports.Transactor = (*Outbox)(nil)
	_ ports.EventSink  = (*Outbox)(nil)
)

type memTx struct {
	owner   *Outbox
	pending []ports.Event
	done    bool
}

func (*memTx) TxKind() string { return "memory" }

// WithTx implements ports.Transactor.
func (o *Outbox) WithTx(ctx context.Context, fn func(ctx context.Context, tx ports.Tx) error) error {
	tx := &memTx{owner: o}
	err := fn(ctx, tx)
	tx.done = true
	if err != nil {
		return err
	}
	o.mu.Lock()
	o.committed = append(o.committed, tx.pending...)
	o.mu.Unlock()
	return nil
}

// ErrForeignTx: a Tx from another Transactor (or a finished one) was used.
var ErrForeignTx = errors.New("memory: transaction does not belong to this outbox or is finished")

// Append implements ports.EventSink.
func (o *Outbox) Append(_ context.Context, tx ports.Tx, ev ports.Event) error {
	mt, ok := tx.(*memTx)
	if !ok || mt.owner != o || mt.done {
		return ErrForeignTx
	}
	if ev.ID == "" || ev.Type == "" {
		return fmt.Errorf("memory: event needs an ID and a type")
	}
	mt.pending = append(mt.pending, ev)
	return nil
}

// Events returns the committed events in order.
func (o *Outbox) Events() []ports.Event {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]ports.Event(nil), o.committed...)
}
