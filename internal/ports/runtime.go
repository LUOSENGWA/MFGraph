package ports

import (
	"context"
	"time"
)

// Clock returns the current time. Business timestamps come from the Clock;
// lock and lease expiry (later slice) use database time instead.
type Clock interface {
	Now() time.Time
}

// ClockFunc adapts a function, such as time.Now, to Clock.
type ClockFunc func() time.Time

// Now calls f.
func (f ClockFunc) Now() time.Time { return f() }

// Event kinds (docs/events.md), the same in both deployment forms. The
// first core slice emits the first five; EventSnapshotCreated is
// registered for explicit snapshots (checkpoints, snapshots before a
// restore), which the first core slice does not have.
const (
	EventWorkspaceCreated = "mfgraph.workspace_created"
	EventBranchCreated    = "mfgraph.branch_created"
	EventCommitCreated    = "mfgraph.commit_created"
	EventBranchUpdated    = "mfgraph.branch_updated"
	EventSnapshotRestored = "mfgraph.snapshot_restored"
	EventSnapshotCreated  = "mfgraph.snapshot_created"
)

// EventKinds lists every registered event kind; EmittedEventKinds those the
// first core slice emits.
var (
	EventKinds = []string{EventWorkspaceCreated, EventBranchCreated, EventCommitCreated, EventSnapshotRestored,
		EventBranchUpdated, EventSnapshotCreated}
	EmittedEventKinds = []string{EventWorkspaceCreated, EventBranchCreated, EventCommitCreated, EventSnapshotRestored,
		EventBranchUpdated}
)

// Event is one outbox row (table outbox_events). It is written with status
// pending in the transaction of the change; module mode forwards it to the
// host platform's outbox afterwards (package outbox).
type Event struct {
	ID          string // event_id of the row, a ULID (the envelope has its own event_id)
	Type        string // the event kind
	WorkspaceID string
	AggregateID string // the business key: snapshot, branch or workspace ID
	Payload     []byte // the envelope (JSON)
	CreatedAtUs int64  // epoch microseconds; the outbox is ordered by (CreatedAtUs, ID)
}

// Tx is an open unit of work created by a Transactor. It is passed to every
// adapter that must write in the same database transaction (business tables
// and the outbox); adapters type-assert it to the concrete type of the
// Transactor they are wired with, and a Tx from another Transactor is an
// error.
type Tx interface {
	// TxKind names the implementation ("mysql", "memory"), for diagnostics.
	TxKind() string
}

// Transactor runs fn in one transaction: it commits when fn returns nil and
// rolls back otherwise.
type Transactor interface {
	WithTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}

// EventSink appends events to the outbox in the same transaction as the
// business writes, so that an event exists if and only if the change was
// committed. Publishing happens later (at least once).
type EventSink interface {
	Append(ctx context.Context, tx Tx, ev Event) error
}

// OutboxQueue is the read side of the outbox, for the publisher of module
// mode (package outbox): the events that are still to be delivered, in the
// order they must be delivered.
type OutboxQueue interface {
	// PendingEvents returns up to limit committed events that are not
	// published yet and have a current kind (mfgraph.*), in outbox order:
	// by CreatedAtUs, then by ID.
	PendingEvents(ctx context.Context, limit int) ([]Event, error)
	// MarkPublished records that an event was delivered; a published event
	// stays published.
	MarkPublished(ctx context.Context, id string, publishedAtUs int64) error
	// MarkFailed records a failed delivery: the event stays pending, its
	// retry count grows by one and nextRetryAtUs says when the publisher
	// tries again.
	MarkFailed(ctx context.Context, id string, nextRetryAtUs int64) error
}

// EventPrefix starts every current event kind; PendingEvents leaves out
// rows of other kinds (written by an earlier version).
const EventPrefix = "mfgraph."

// Outbox row states.
const (
	EventPending   = "pending"
	EventPublished = "published"
)

// EventDelivery is the delivery state of an outbox row.
type EventDelivery struct {
	Status        string // EventPending or EventPublished
	RetryCount    int
	NextRetryAtUs int64 // 0 = NULL
	PublishedAtUs int64 // 0 = NULL
}
