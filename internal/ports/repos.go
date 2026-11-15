package ports

import (
	"context"
	"errors"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
)

var (
	// ErrAlreadyExists: a row with the same key (or the same unique value)
	// exists. Repositories return it for duplicate inserts.
	ErrAlreadyExists = errors.New("already exists")
	// ErrDBUnavailable: the database cannot be reached. The API answers 503
	// db_unavailable.
	ErrDBUnavailable = errors.New("database unavailable")
)

// Row states used by the first core slice.
const (
	StatusActive = "active"

	BlobStaged = "staged" // uploaded, not yet referenced by a snapshot
	BlobLive   = "live"   // referenced by at least one snapshot

	IdempotencyInProgress = "in_progress"
	IdempotencyCompleted  = "completed"

	WorkspaceTypeProject = "project"
	BranchTypeMain       = "main"
	MainRefName          = "main"

	// Snapshot trigger kinds (contract: Snapshot.trigger_kind).
	TriggerManual  = "manual"  // a commit
	TriggerRestore = "restore" // a restore
)

// Times are epoch microseconds (BIGINT columns), taken from the Clock.

// Project is a row of the kernel table projects (standalone deployments).
// OwnerRef is the actor reference of the project's creator; it is empty for
// projects without a recorded owner, which nobody can access in a
// standalone deployment.
type Project struct {
	ID                string
	TenantID          string
	Name              string
	Status            string
	OwnerRef          string
	OwnerPrincipalUID string // "" = NULL
	CreatedAtUs       int64
	UpdatedAtUs       int64
}

// Workspace is a row of workspaces. StoragePrefix never changes.
type Workspace struct {
	ID                string
	TenantID          string
	ProjectID         string
	Type              string
	OwnerRef          string
	OwnerPrincipalUID string // "" = NULL
	StorageBackendID  string
	StoragePrefix     string
	StoragePrefixKey  string // storagekey.PrefixKey(StoragePrefix)
	DisplayName       string
	Status            string
	CreatedAtUs       int64
	UpdatedAtUs       int64
}

// Branch is a row of branches. HeadSnapshotID is "" for a branch without
// commits; WorkstreamID is "" for the main branch.
type Branch struct {
	ID                    string
	WorkspaceID           string
	ProjectID             string
	WorkstreamID          string
	Name                  string // branch_name, compared byte for byte
	RefName               string // ref_name, compared byte for byte
	Type                  string
	HeadSnapshotID        string
	Generation            int64
	Status                string
	CreatedByRef          string
	CreatedByPrincipalUID string // "" = NULL
	CreatedAtUs           int64
	UpdatedAtUs           int64
}

// Snapshot is a row of snapshots together with its parents (rows of
// snapshot_parents, in parent order).
type Snapshot struct {
	ID                 string
	WorkspaceID        string
	ProjectID          string
	BranchID           string // "" = NULL
	RefName            string
	AuthorRef          string
	AuthorPrincipalUID string // "" = NULL
	Message            string
	TriggerKind        string
	RootTreeHash       contenthash.Hash
	TreeFormatVersion  int
	ParentIDs          []string
	EntryCount         int64 // number of files
	TotalBytes         int64 // sum of the file sizes
	CreatedAtUs        int64
}

// Blob is a row of blobs: one content object of a workspace. StorageKey is
// relative to the workspace storage prefix (storagekey.ObjectKey).
type Blob struct {
	WorkspaceID string
	Hash        contenthash.Hash
	Size        int64
	StorageKey  string
	Status      string
	CreatedAtUs int64
}

// IdempotencyRecord is a row of idempotency_keys, keyed by (ActorRef,
// Route, Key). RequestHash is the lowercase hex SHA-256 of the normalized
// request; Response is the stored response body once Status is completed.
type IdempotencyRecord struct {
	ActorRef    string
	Route       string
	Key         string
	RequestHash string
	Status      string
	Response    []byte
	CreatedAtUs int64
	ExpiresAtUs int64
}

// TimelineCursor is a keyset position in a timeline (newest first): the
// next page starts after (CreatedAtUs, SnapshotID).
type TimelineCursor struct {
	CreatedAtUs int64
	SnapshotID  string
}

// SnapshotQuery selects a page of a workspace's snapshots, newest first
// (created_at_us, then snapshot_id, both descending).
type SnapshotQuery struct {
	WorkspaceID string
	BranchID    string          // optional: only snapshots created on this branch
	Before      *TimelineCursor // optional: only snapshots after this position
	Limit       int
}

// Repositories. Every write takes the Tx of a Transactor.WithTx call. Reads
// take a Tx too, which may be nil: with a Tx they see the writes of that
// transaction, without one they read committed data. Lookups of missing
// rows return ErrNotFound; inserts of duplicate keys return
// ErrAlreadyExists. IDs, actor references, names and refs are compared byte
// for byte.

// ProjectRepo stores projects.
type ProjectRepo interface {
	InsertProject(ctx context.Context, tx Tx, p Project) error
	GetProject(ctx context.Context, tx Tx, id string) (Project, error)
}

// WorkspaceRepo stores workspaces. InsertWorkspace returns ErrAlreadyExists
// when the ID exists or another workspace on the same storage backend has
// the same StoragePrefixKey.
type WorkspaceRepo interface {
	InsertWorkspace(ctx context.Context, tx Tx, w Workspace) error
	GetWorkspace(ctx context.Context, tx Tx, id string) (Workspace, error)
}

// BranchRepo stores branches.
type BranchRepo interface {
	// InsertBranch returns ErrAlreadyExists when the ID or (WorkspaceID,
	// RefName) exists.
	InsertBranch(ctx context.Context, tx Tx, b Branch) error
	GetBranch(ctx context.Context, tx Tx, id string) (Branch, error)
	GetBranchByRef(ctx context.Context, tx Tx, workspaceID, refName string) (Branch, error)
	// ListBranches returns up to limit branches of a workspace whose ref
	// name sorts after afterRef (byte order), ordered by ref name.
	ListBranches(ctx context.Context, tx Tx, workspaceID, afterRef string, limit int) ([]Branch, error)
	// UpdateBranchHead is the compare-and-swap of a commit: it sets the head,
	// increments the generation and sets updated_at_us only if the branch
	// belongs to workspaceID and its generation is expectedGeneration. It
	// reports whether the row was updated.
	UpdateBranchHead(ctx context.Context, tx Tx, branchID, workspaceID string, expectedGeneration int64,
		headSnapshotID string, updatedAtUs int64) (bool, error)
}

// UsageRepo stores the per-workspace usage ledger (workspace_usage). The
// first core slice only creates the row (all counters 0).
type UsageRepo interface {
	InsertUsage(ctx context.Context, tx Tx, workspaceID string, updatedAtUs int64) error
}

// SnapshotRepo stores snapshots and their parents.
type SnapshotRepo interface {
	// InsertSnapshot inserts the snapshot and one snapshot_parents row per
	// parent (parent_order = index in ParentIDs).
	InsertSnapshot(ctx context.Context, tx Tx, s Snapshot) error
	GetSnapshot(ctx context.Context, tx Tx, id string) (Snapshot, error)
	ListSnapshots(ctx context.Context, tx Tx, q SnapshotQuery) ([]Snapshot, error)
}

// BlobRepo stores the content objects of workspaces.
type BlobRepo interface {
	// InsertBlob returns ErrAlreadyExists when (WorkspaceID, Hash) exists.
	InsertBlob(ctx context.Context, tx Tx, b Blob) error
	GetBlob(ctx context.Context, tx Tx, workspaceID string, h contenthash.Hash) (Blob, error)
	// GetBlobs returns the rows that exist among hashes; missing hashes are
	// absent from the map.
	GetBlobs(ctx context.Context, tx Tx, workspaceID string, hashes []contenthash.Hash) (map[contenthash.Hash]Blob, error)
	// MarkBlobsLive sets the status of the given staged rows to live.
	MarkBlobsLive(ctx context.Context, tx Tx, workspaceID string, hashes []contenthash.Hash) error
}

// IdempotencyRepo stores idempotency records. A record is identified by
// (actorRef, route, key); createdAtUs identifies one instance of it, so that
// a request only completes or deletes the record it inserted itself.
type IdempotencyRepo interface {
	// InsertIdempotency returns ErrAlreadyExists when a record exists.
	InsertIdempotency(ctx context.Context, tx Tx, rec IdempotencyRecord) error
	GetIdempotency(ctx context.Context, tx Tx, actorRef, route, key string) (IdempotencyRecord, error)
	// CompleteIdempotency stores the response and marks the in-progress
	// record created at createdAtUs as completed; ErrNotFound when there is
	// no such record.
	CompleteIdempotency(ctx context.Context, tx Tx, actorRef, route, key string, createdAtUs int64, response []byte) error
	// DeleteIdempotency deletes the record created at createdAtUs if its
	// status is status; deleting nothing is not an error.
	DeleteIdempotency(ctx context.Context, tx Tx, actorRef, route, key string, createdAtUs int64, status string) error
}

// Store is everything the core persists in its database: the repositories,
// the transaction boundary and the outbox. The in-memory implementation
// (ports/memory) and the MySQL one (store/mysql) pass the same contract
// tests (ports/porttest).
type Store interface {
	Transactor
	EventSink
	ProjectRepo
	WorkspaceRepo
	BranchRepo
	UsageRepo
	SnapshotRepo
	BlobRepo
	IdempotencyRepo
}
