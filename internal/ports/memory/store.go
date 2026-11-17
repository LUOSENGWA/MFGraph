package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

// Store is an in-memory ports.Store for unit tests. Transactions are
// serialized and see a private copy of the data, which replaces the
// committed data when the transaction commits; reads without a transaction
// see committed data only. It implements the same contract as the MySQL
// store (ports/porttest) but is not durable.
type Store struct {
	txMu sync.Mutex   // serializes transactions
	mu   sync.RWMutex // guards cur
	cur  *state
}

var _ ports.Store = (*Store)(nil)

// NewStore returns an empty store.
func NewStore() *Store { return &Store{cur: newState()} }

type blobKey struct {
	ws string
	h  contenthash.Hash
}

type idemKey struct{ actor, route, key string }

type state struct {
	projects   map[string]ports.Project
	workspaces map[string]ports.Workspace
	prefixKeys map[[2]string]string // (backend, prefix key) -> workspace ID
	branches   map[string]ports.Branch
	branchRefs map[[2]string]string // (workspace ID, ref name) -> branch ID
	usage      map[string]int64     // workspace ID -> updated_at_us
	snapshots  map[string]ports.Snapshot
	blobs      map[blobKey]ports.Blob
	idem       map[idemKey]ports.IdempotencyRecord
	events     []ports.Event
	delivery   map[string]ports.EventDelivery // by event ID; absent = pending, never tried
}

func newState() *state {
	return &state{
		projects:   map[string]ports.Project{},
		workspaces: map[string]ports.Workspace{},
		prefixKeys: map[[2]string]string{},
		branches:   map[string]ports.Branch{},
		branchRefs: map[[2]string]string{},
		usage:      map[string]int64{},
		snapshots:  map[string]ports.Snapshot{},
		blobs:      map[blobKey]ports.Blob{},
		idem:       map[idemKey]ports.IdempotencyRecord{},
		delivery:   map[string]ports.EventDelivery{},
	}
}

func cloneMap[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// clone copies the maps; the values are copied by value, and the slices
// they contain (Snapshot.ParentIDs, IdempotencyRecord.Response) are never
// modified in place.
func (s *state) clone() *state {
	return &state{
		projects:   cloneMap(s.projects),
		workspaces: cloneMap(s.workspaces),
		prefixKeys: cloneMap(s.prefixKeys),
		branches:   cloneMap(s.branches),
		branchRefs: cloneMap(s.branchRefs),
		usage:      cloneMap(s.usage),
		snapshots:  cloneMap(s.snapshots),
		blobs:      cloneMap(s.blobs),
		idem:       cloneMap(s.idem),
		events:     append([]ports.Event(nil), s.events...),
		delivery:   cloneMap(s.delivery),
	}
}

type storeTx struct {
	store *Store
	st    *state
	done  bool
}

func (*storeTx) TxKind() string { return "memory" }

// WithTx implements ports.Transactor.
func (s *Store) WithTx(ctx context.Context, fn func(ctx context.Context, tx ports.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.txMu.Lock()
	defer s.txMu.Unlock()
	s.mu.RLock()
	work := s.cur.clone()
	s.mu.RUnlock()
	tx := &storeTx{store: s, st: work}
	defer func() { tx.done = true }()
	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.cur = work
	s.mu.Unlock()
	return nil
}

// read returns the state a read sees: the transaction's copy, or the
// committed state (with its read lock held until release is called).
func (s *Store) read(tx ports.Tx) (st *state, release func(), err error) {
	if tx == nil {
		s.mu.RLock()
		return s.cur, s.mu.RUnlock, nil
	}
	mt, ok := tx.(*storeTx)
	if !ok || mt.store != s || mt.done {
		return nil, nil, ErrForeignTx
	}
	return mt.st, func() {}, nil
}

// write returns the transaction's copy; writes need a transaction.
func (s *Store) write(tx ports.Tx) (*state, error) {
	mt, ok := tx.(*storeTx)
	if !ok || mt.store != s || mt.done {
		return nil, ErrForeignTx
	}
	return mt.st, nil
}

func notFound(what, id string) error { return fmt.Errorf("%w: %s %q", ports.ErrNotFound, what, id) }

func exists(what string) error { return fmt.Errorf("%w: %s", ports.ErrAlreadyExists, what) }

// ---- projects ---------------------------------------------------------------

// InsertProject implements ports.ProjectRepo.
func (s *Store) InsertProject(_ context.Context, tx ports.Tx, p ports.Project) error {
	st, err := s.write(tx)
	if err != nil {
		return err
	}
	if _, dup := st.projects[p.ID]; dup {
		return exists("project " + p.ID)
	}
	st.projects[p.ID] = p
	return nil
}

// GetProject implements ports.ProjectRepo.
func (s *Store) GetProject(_ context.Context, tx ports.Tx, id string) (ports.Project, error) {
	st, release, err := s.read(tx)
	if err != nil {
		return ports.Project{}, err
	}
	defer release()
	p, ok := st.projects[id]
	if !ok {
		return ports.Project{}, notFound("project", id)
	}
	return p, nil
}

// ---- workspaces -------------------------------------------------------------

// InsertWorkspace implements ports.WorkspaceRepo.
func (s *Store) InsertWorkspace(_ context.Context, tx ports.Tx, w ports.Workspace) error {
	st, err := s.write(tx)
	if err != nil {
		return err
	}
	pk := [2]string{w.StorageBackendID, w.StoragePrefixKey}
	if _, dup := st.workspaces[w.ID]; dup {
		return exists("workspace " + w.ID)
	}
	if _, dup := st.prefixKeys[pk]; dup {
		return exists("storage prefix on backend " + w.StorageBackendID)
	}
	st.workspaces[w.ID] = w
	st.prefixKeys[pk] = w.ID
	return nil
}

// GetWorkspace implements ports.WorkspaceRepo.
func (s *Store) GetWorkspace(_ context.Context, tx ports.Tx, id string) (ports.Workspace, error) {
	st, release, err := s.read(tx)
	if err != nil {
		return ports.Workspace{}, err
	}
	defer release()
	w, ok := st.workspaces[id]
	if !ok {
		return ports.Workspace{}, notFound("workspace", id)
	}
	return w, nil
}

// ---- branches ---------------------------------------------------------------

// InsertBranch implements ports.BranchRepo.
func (s *Store) InsertBranch(_ context.Context, tx ports.Tx, b ports.Branch) error {
	st, err := s.write(tx)
	if err != nil {
		return err
	}
	rk := [2]string{b.WorkspaceID, b.RefName}
	if _, dup := st.branches[b.ID]; dup {
		return exists("branch " + b.ID)
	}
	if _, dup := st.branchRefs[rk]; dup {
		return exists("branch ref of workspace " + b.WorkspaceID)
	}
	st.branches[b.ID] = b
	st.branchRefs[rk] = b.ID
	return nil
}

// GetBranch implements ports.BranchRepo.
func (s *Store) GetBranch(_ context.Context, tx ports.Tx, id string) (ports.Branch, error) {
	st, release, err := s.read(tx)
	if err != nil {
		return ports.Branch{}, err
	}
	defer release()
	b, ok := st.branches[id]
	if !ok {
		return ports.Branch{}, notFound("branch", id)
	}
	return b, nil
}

// GetBranchByRef implements ports.BranchRepo.
func (s *Store) GetBranchByRef(_ context.Context, tx ports.Tx, workspaceID, refName string) (ports.Branch, error) {
	st, release, err := s.read(tx)
	if err != nil {
		return ports.Branch{}, err
	}
	defer release()
	id, ok := st.branchRefs[[2]string{workspaceID, refName}]
	if !ok {
		return ports.Branch{}, notFound("branch ref", refName)
	}
	return st.branches[id], nil
}

// ListBranches implements ports.BranchRepo.
func (s *Store) ListBranches(_ context.Context, tx ports.Tx, workspaceID, afterRef string, limit int) ([]ports.Branch, error) {
	st, release, err := s.read(tx)
	if err != nil {
		return nil, err
	}
	defer release()
	var out []ports.Branch
	for _, b := range st.branches {
		if b.WorkspaceID == workspaceID && b.RefName > afterRef {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RefName < out[j].RefName })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// UpdateBranchHead implements ports.BranchRepo.
func (s *Store) UpdateBranchHead(_ context.Context, tx ports.Tx, branchID, workspaceID string, expectedGeneration int64,
	headSnapshotID string, updatedAtUs int64) (bool, error) {
	st, err := s.write(tx)
	if err != nil {
		return false, err
	}
	b, ok := st.branches[branchID]
	if !ok || b.WorkspaceID != workspaceID || b.Generation != expectedGeneration {
		return false, nil
	}
	b.HeadSnapshotID = headSnapshotID
	b.Generation++
	b.UpdatedAtUs = updatedAtUs
	st.branches[branchID] = b
	return true, nil
}

// ---- usage ------------------------------------------------------------------

// InsertUsage implements ports.UsageRepo.
func (s *Store) InsertUsage(_ context.Context, tx ports.Tx, workspaceID string, updatedAtUs int64) error {
	st, err := s.write(tx)
	if err != nil {
		return err
	}
	if _, dup := st.usage[workspaceID]; dup {
		return exists("usage of workspace " + workspaceID)
	}
	st.usage[workspaceID] = updatedAtUs
	return nil
}

// HasUsage reports whether the usage row of a workspace exists (test helper).
func (s *Store) HasUsage(workspaceID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.cur.usage[workspaceID]
	return ok
}

// ---- snapshots --------------------------------------------------------------

// InsertSnapshot implements ports.SnapshotRepo.
func (s *Store) InsertSnapshot(_ context.Context, tx ports.Tx, snap ports.Snapshot) error {
	st, err := s.write(tx)
	if err != nil {
		return err
	}
	if _, dup := st.snapshots[snap.ID]; dup {
		return exists("snapshot " + snap.ID)
	}
	seen := map[string]bool{}
	for _, p := range snap.ParentIDs {
		if seen[p] {
			return exists("snapshot parent " + p)
		}
		seen[p] = true
	}
	snap.ParentIDs = append([]string{}, snap.ParentIDs...)
	st.snapshots[snap.ID] = snap
	return nil
}

func copySnapshot(s ports.Snapshot) ports.Snapshot {
	s.ParentIDs = append([]string{}, s.ParentIDs...)
	return s
}

// GetSnapshot implements ports.SnapshotRepo.
func (s *Store) GetSnapshot(_ context.Context, tx ports.Tx, id string) (ports.Snapshot, error) {
	st, release, err := s.read(tx)
	if err != nil {
		return ports.Snapshot{}, err
	}
	defer release()
	snap, ok := st.snapshots[id]
	if !ok {
		return ports.Snapshot{}, notFound("snapshot", id)
	}
	return copySnapshot(snap), nil
}

// ListSnapshots implements ports.SnapshotRepo.
func (s *Store) ListSnapshots(_ context.Context, tx ports.Tx, q ports.SnapshotQuery) ([]ports.Snapshot, error) {
	st, release, err := s.read(tx)
	if err != nil {
		return nil, err
	}
	defer release()
	var out []ports.Snapshot
	for _, snap := range st.snapshots {
		if snap.WorkspaceID != q.WorkspaceID || q.BranchID != "" && snap.BranchID != q.BranchID {
			continue
		}
		if c := q.Before; c != nil && !(snap.CreatedAtUs < c.CreatedAtUs ||
			snap.CreatedAtUs == c.CreatedAtUs && snap.ID < c.SnapshotID) {
			continue
		}
		out = append(out, copySnapshot(snap))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAtUs != out[j].CreatedAtUs {
			return out[i].CreatedAtUs > out[j].CreatedAtUs
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// ---- blobs ------------------------------------------------------------------

// InsertBlob implements ports.BlobRepo.
func (s *Store) InsertBlob(_ context.Context, tx ports.Tx, b ports.Blob) error {
	st, err := s.write(tx)
	if err != nil {
		return err
	}
	k := blobKey{b.WorkspaceID, b.Hash}
	if _, dup := st.blobs[k]; dup {
		return exists("blob " + b.Hash.String())
	}
	st.blobs[k] = b
	return nil
}

// GetBlob implements ports.BlobRepo.
func (s *Store) GetBlob(_ context.Context, tx ports.Tx, workspaceID string, h contenthash.Hash) (ports.Blob, error) {
	st, release, err := s.read(tx)
	if err != nil {
		return ports.Blob{}, err
	}
	defer release()
	b, ok := st.blobs[blobKey{workspaceID, h}]
	if !ok {
		return ports.Blob{}, notFound("blob", h.String())
	}
	return b, nil
}

// GetBlobs implements ports.BlobRepo.
func (s *Store) GetBlobs(_ context.Context, tx ports.Tx, workspaceID string, hashes []contenthash.Hash) (map[contenthash.Hash]ports.Blob, error) {
	st, release, err := s.read(tx)
	if err != nil {
		return nil, err
	}
	defer release()
	out := map[contenthash.Hash]ports.Blob{}
	for _, h := range hashes {
		if b, ok := st.blobs[blobKey{workspaceID, h}]; ok {
			out[h] = b
		}
	}
	return out, nil
}

// MarkBlobsLive implements ports.BlobRepo.
func (s *Store) MarkBlobsLive(_ context.Context, tx ports.Tx, workspaceID string, hashes []contenthash.Hash) error {
	st, err := s.write(tx)
	if err != nil {
		return err
	}
	for _, h := range hashes {
		k := blobKey{workspaceID, h}
		if b, ok := st.blobs[k]; ok && b.Status == ports.BlobStaged {
			b.Status = ports.BlobLive
			st.blobs[k] = b
		}
	}
	return nil
}

// ---- idempotency ------------------------------------------------------------

// InsertIdempotency implements ports.IdempotencyRepo.
func (s *Store) InsertIdempotency(_ context.Context, tx ports.Tx, rec ports.IdempotencyRecord) error {
	st, err := s.write(tx)
	if err != nil {
		return err
	}
	k := idemKey{rec.ActorRef, rec.Route, rec.Key}
	if _, dup := st.idem[k]; dup {
		return exists("idempotency record")
	}
	rec.Response = append([]byte(nil), rec.Response...)
	st.idem[k] = rec
	return nil
}

// GetIdempotency implements ports.IdempotencyRepo.
func (s *Store) GetIdempotency(_ context.Context, tx ports.Tx, actorRef, route, key string) (ports.IdempotencyRecord, error) {
	st, release, err := s.read(tx)
	if err != nil {
		return ports.IdempotencyRecord{}, err
	}
	defer release()
	rec, ok := st.idem[idemKey{actorRef, route, key}]
	if !ok {
		return ports.IdempotencyRecord{}, notFound("idempotency record", key)
	}
	rec.Response = append([]byte(nil), rec.Response...)
	return rec, nil
}

// CompleteIdempotency implements ports.IdempotencyRepo.
func (s *Store) CompleteIdempotency(_ context.Context, tx ports.Tx, actorRef, route, key string, createdAtUs int64, response []byte) error {
	st, err := s.write(tx)
	if err != nil {
		return err
	}
	k := idemKey{actorRef, route, key}
	rec, ok := st.idem[k]
	if !ok || rec.CreatedAtUs != createdAtUs || rec.Status != ports.IdempotencyInProgress {
		return notFound("in-progress idempotency record", key)
	}
	rec.Status = ports.IdempotencyCompleted
	rec.Response = append([]byte(nil), response...)
	st.idem[k] = rec
	return nil
}

// DeleteIdempotency implements ports.IdempotencyRepo.
func (s *Store) DeleteIdempotency(_ context.Context, tx ports.Tx, actorRef, route, key string, createdAtUs int64, status string) error {
	st, err := s.write(tx)
	if err != nil {
		return err
	}
	k := idemKey{actorRef, route, key}
	if rec, ok := st.idem[k]; ok && rec.CreatedAtUs == createdAtUs && rec.Status == status {
		delete(st.idem, k)
	}
	return nil
}

// ---- outbox -----------------------------------------------------------------

// Append implements ports.EventSink: the event becomes visible when the
// transaction commits.
func (s *Store) Append(_ context.Context, tx ports.Tx, ev ports.Event) error {
	st, err := s.write(tx)
	if err != nil {
		return err
	}
	if ev.ID == "" || ev.Type == "" {
		return fmt.Errorf("memory: event needs an ID and a type")
	}
	for _, e := range st.events {
		if e.ID == ev.ID {
			return exists("event " + ev.ID)
		}
	}
	ev.Payload = append([]byte(nil), ev.Payload...)
	st.events = append(st.events, ev)
	return nil
}

// Events returns the committed outbox events in the order they were
// appended.
func (s *Store) Events(context.Context) ([]ports.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ports.Event, len(s.cur.events))
	copy(out, s.cur.events)
	return out, nil
}

// PendingEvents implements ports.OutboxQueue.
func (s *Store) PendingEvents(_ context.Context, limit int) ([]ports.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []ports.Event
	for _, ev := range s.cur.events {
		if s.cur.delivery[ev.ID].Status != ports.EventPublished && strings.HasPrefix(ev.Type, ports.EventPrefix) {
			out = append(out, ev)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAtUs != out[j].CreatedAtUs {
			return out[i].CreatedAtUs < out[j].CreatedAtUs
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// MarkPublished implements ports.OutboxQueue.
func (s *Store) MarkPublished(_ context.Context, id string, publishedAtUs int64) error {
	s.updateDelivery(id, func(d *ports.EventDelivery) {
		if d.Status != ports.EventPublished {
			d.Status, d.PublishedAtUs = ports.EventPublished, publishedAtUs
		}
	})
	return nil
}

// MarkFailed implements ports.OutboxQueue.
func (s *Store) MarkFailed(_ context.Context, id string, nextRetryAtUs int64) error {
	s.updateDelivery(id, func(d *ports.EventDelivery) {
		if d.Status != ports.EventPublished {
			d.RetryCount++
			d.NextRetryAtUs = nextRetryAtUs
		}
	})
	return nil
}

// EventDelivery returns the delivery state of an outbox event.
func (s *Store) EventDelivery(_ context.Context, id string) (ports.EventDelivery, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ev := range s.cur.events {
		if ev.ID == id {
			d := s.cur.delivery[id]
			if d.Status == "" {
				d.Status = ports.EventPending
			}
			return d, nil
		}
	}
	return ports.EventDelivery{}, notFound("event", id)
}

// updateDelivery changes the delivery state of a committed event between
// transactions (txMu keeps a transaction from replacing the change).
func (s *Store) updateDelivery(id string, fn func(*ports.EventDelivery)) {
	s.txMu.Lock()
	defer s.txMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range s.cur.events {
		if ev.ID == id {
			d := s.cur.delivery[id]
			if d.Status == "" {
				d.Status = ports.EventPending
			}
			fn(&d)
			s.cur.delivery[id] = d
			return
		}
	}
}
