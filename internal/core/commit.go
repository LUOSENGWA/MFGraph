package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/pathx"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/tree"
)

// Change is one change of a commit: a content hash (with an optional
// mode), or Delete.
type Change struct {
	Path   string  `json:"path"`
	Hash   *string `json:"hash,omitempty"`
	Mode   *int    `json:"mode,omitempty"`
	Delete *bool   `json:"delete,omitempty"`
}

// CommitRequest is the body of POST /workspaces/{workspace_id}/commits.
type CommitRequest struct {
	BranchID           string   `json:"branch_id"`
	ExpectedGeneration int64    `json:"expected_generation"`
	Message            string   `json:"message"`
	Changes            []Change `json:"changes"`
}

// RestoreRequest is the body of POST /snapshots/{snapshot_id}/restore.
type RestoreRequest struct {
	BranchID           string  `json:"branch_id"`
	ExpectedGeneration int64   `json:"expected_generation"`
	Message            string  `json:"message"`
	Path               *string `json:"path,omitempty"`
}

// CommitResult is the new snapshot of a commit or a restore.
type CommitResult struct {
	SnapshotID   string   `json:"snapshot_id"`
	BranchID     string   `json:"branch_id"`
	RefName      string   `json:"ref_name"`
	Generation   int64    `json:"generation"`
	RootTreeHash string   `json:"root_tree_hash"`
	ParentIDs    []string `json:"parent_ids"`
	RestoredFrom string   `json:"restored_from,omitempty"`
	CreatedAtUs  int64    `json:"created_at_us"`
}

const maxMessage = 1024

func checkTarget(branchID string, expectedGeneration int64, message string) error {
	if err := checkID("branch_id", branchID); err != nil {
		return err
	}
	if expectedGeneration < 1 {
		return invalid("expected_generation must be at least 1")
	}
	return checkLength("message", message, maxMessage)
}

// changes validates the request's changes and converts them (paths are
// checked by tree.Apply).
func (s *Service) changes(cs []Change) ([]tree.Change, []contenthash.Hash, error) {
	if len(cs) == 0 {
		return nil, nil, invalid("changes must not be empty")
	}
	if len(cs) > s.cfg.MaxCommitChanges {
		return nil, nil, newError(CodeInvalidRequest, fmt.Sprintf("a commit has at most %d changes", s.cfg.MaxCommitChanges),
			map[string]any{"reason": ReasonTooManyChanges, "max_changes": s.cfg.MaxCommitChanges})
	}
	out := make([]tree.Change, len(cs))
	seen := map[contenthash.Hash]bool{}
	var hashes []contenthash.Hash
	for i, c := range cs {
		tc := tree.Change{Path: c.Path}
		switch {
		case c.Delete != nil && c.Hash != nil:
			return nil, nil, invalid(fmt.Sprintf("change %d has both hash and delete", i+1))
		case c.Delete != nil:
			if !*c.Delete {
				return nil, nil, invalid(fmt.Sprintf("change %d: delete must be true when present", i+1))
			}
			if c.Mode != nil {
				return nil, nil, invalid(fmt.Sprintf("change %d: a delete has no mode", i+1))
			}
			tc.Delete = true
		case c.Hash != nil:
			h, err := contenthash.Parse(*c.Hash)
			if err != nil {
				return nil, nil, invalid(fmt.Sprintf("change %d: hash must be sha256: followed by 64 lowercase hex digits", i+1))
			}
			tc.Hash = h
			if c.Mode != nil {
				if *c.Mode != int(tree.ModeRegular) && *c.Mode != int(tree.ModeExecutable) {
					return nil, nil, invalid(fmt.Sprintf("change %d: mode must be 420 or 493", i+1))
				}
				tc.Mode = uint32(*c.Mode)
			}
			if !seen[h] {
				seen[h] = true
				hashes = append(hashes, h)
			}
		default:
			return nil, nil, invalid(fmt.Sprintf("change %d needs hash or delete", i+1))
		}
		out[i] = tc
	}
	return out, hashes, nil
}

// base is the head of a branch: its snapshot, root and counts.
type base struct {
	snap ports.Snapshot // zero for a branch without commits
	root contenthash.Hash
}

func (s *Service) headOf(ctx context.Context, b ports.Branch) (base, error) {
	if b.HeadSnapshotID == "" {
		return base{root: tree.EmptyRoot}, nil
	}
	snap, err := s.d.Store.GetSnapshot(ctx, nil, b.HeadSnapshotID)
	if err != nil {
		return base{}, fmt.Errorf("core: head snapshot of branch %s: %w", b.ID, mapErr(err, "head snapshot"))
	}
	return base{snap: snap, root: snap.RootTreeHash}, nil
}

func generationConflict(current int64) *Error {
	return newError(CodeConflict, "expected_generation is not the current generation of the branch",
		map[string]any{"current_generation": current})
}

// Commit applies changes to the head of a branch and creates a snapshot
// (specification, section 2.6; the design's commit protocol):
//
//  1. the caller must own the workspace, and the branch must belong to it;
//  2. the base is the root tree of the branch head (empty without one);
//  3. every hash must be a blob of this workspace (410 blob_missing,
//     reason not_uploaded) whose object exists in storage with its size
//     (reason missing_in_storage);
//  4. tree.Apply builds the new tree, rewriting only the changed paths; a
//     tree equal to the base is refused (no_changes);
//  5. the new tree nodes are stored;
//  6. one transaction inserts the snapshot and its parent, moves the
//     branch head with a compare-and-swap on the expected generation,
//     marks the blobs live, appends snapshot.created and branch.updated to
//     the outbox and completes the idempotency record.
//
// A failure before step 5 leaves nothing behind; after it, unreferenced
// tree nodes and objects remain for garbage collection. The database never
// holds a partial snapshot.
func (s *Service) Commit(ctx context.Context, p ports.Principal, workspaceID string, req CommitRequest, idemKey string) (CommitResult, error) {
	if err := checkID("workspace_id", workspaceID); err != nil {
		return CommitResult{}, err
	}
	if err := checkTarget(req.BranchID, req.ExpectedGeneration, req.Message); err != nil {
		return CommitResult{}, err
	}
	changes, hashes, err := s.changes(req.Changes)
	if err != nil {
		return CommitResult{}, err
	}
	ws, _, err := s.access(ctx, p, workspaceID, ports.ActionWorkspaceWrite)
	if err != nil {
		return CommitResult{}, err
	}
	branch, err := s.branchOf(ctx, ws.ID, req.BranchID)
	if err != nil {
		return CommitResult{}, err
	}
	hashed := struct {
		WorkspaceID string        `json:"workspace_id"`
		Request     CommitRequest `json:"request"`
	}{workspaceID, req}
	return idempotent(ctx, s, p, RouteCommit, idemKey, hashed, func(ctx context.Context, complete completer) (CommitResult, error) {
		if branch.Generation != req.ExpectedGeneration {
			return CommitResult{}, generationConflict(branch.Generation)
		}
		head, err := s.headOf(ctx, branch)
		if err != nil {
			return CommitResult{}, err
		}
		blobs, err := s.checkBlobs(ctx, ws, hashes)
		if err != nil {
			return CommitResult{}, err
		}
		for i := range changes {
			if !changes[i].Delete {
				changes[i].Size = blobs[changes[i].Hash].Size
			}
		}
		res, err := tree.Apply(ctx, s.getter(ws.ID), head.root, changes)
		if err != nil {
			return CommitResult{}, treeError(err)
		}
		if res.Root == head.root {
			return CommitResult{}, invalidReason(ReasonNoChanges, "the changes do not change the tree")
		}
		if err := s.putNodes(ctx, ws.ID, res.Nodes); err != nil {
			return CommitResult{}, err
		}
		return s.record(ctx, p, ws, branch, head, record{
			root: res.Root, delta: res.Delta, trigger: ports.TriggerManual, message: req.Message,
			live: hashes, event: ports.EventSnapshotCreated,
		}, complete)
	})
}

// checkBlobs returns the blobs of the hashes, or blob_missing.
func (s *Service) checkBlobs(ctx context.Context, ws ports.Workspace, hashes []contenthash.Hash) (map[contenthash.Hash]ports.Blob, error) {
	blobs, err := s.d.Store.GetBlobs(ctx, nil, ws.ID, hashes)
	if err != nil {
		return nil, mapErr(err, "blobs")
	}
	var missing []string
	for _, h := range hashes {
		if _, ok := blobs[h]; !ok {
			missing = append(missing, h.String())
		}
	}
	if len(missing) > 0 {
		return nil, blobMissing(ReasonNotUploaded, "content not uploaded to this workspace", missing)
	}
	for _, h := range hashes {
		info, err := s.d.Objects.Stat(ctx, storageKey(ws, blobs[h]))
		switch {
		case errors.Is(err, ports.ErrNotFound) || err == nil && info.Size != blobs[h].Size:
			missing = append(missing, h.String())
		case err != nil:
			return nil, mapErr(err, "object")
		}
	}
	if len(missing) > 0 {
		return nil, blobMissing(ReasonMissingInStorage, "content missing from storage", missing)
	}
	return blobs, nil
}

func blobMissing(reason, message string, hashes []string) *Error {
	sort.Strings(hashes)
	return newError(CodeBlobMissing, message, map[string]any{"reason": reason, "hashes": hashes})
}

func storageKey(ws ports.Workspace, b ports.Blob) string {
	return ws.StoragePrefix + "/" + b.StorageKey
}

// treeError maps the errors of tree.Apply and tree.Graft.
func treeError(err error) error {
	var pe *pathx.Error
	switch {
	case errors.As(err, &pe):
		return err // invalid_path with details.kind
	case errors.Is(err, tree.ErrPathNotFound):
		return &Error{Code: CodeConflict, Message: "the path is not a file of the base snapshot",
			Details: map[string]any{"reason": ReasonPathNotFound}, Err: err}
	case errors.Is(err, tree.ErrInvalidEntry):
		return &Error{Code: CodeInvalidRequest, Message: err.Error(), Err: err}
	}
	return mapErr(err, "tree node")
}

// putNodes stores the new tree nodes before the database refers to them.
func (s *Service) putNodes(ctx context.Context, workspaceID string, nodes map[contenthash.Hash][]byte) error {
	hs := make([]contenthash.Hash, 0, len(nodes))
	for h := range nodes {
		hs = append(hs, h)
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i].Hex() < hs[j].Hex() })
	for _, h := range hs {
		if err := s.d.Trees.Put(ctx, workspaceID, h, nodes[h]); err != nil {
			return mapErr(err, "tree node")
		}
	}
	return nil
}

// record is a new snapshot to record.
type record struct {
	root         contenthash.Hash
	delta        tree.Stats
	counts       *tree.Stats // the new counts, when not base + delta
	trigger      string
	message      string
	restoredFrom string
	live         []contenthash.Hash
	event        string
}

// record runs the transaction of a commit or a restore. A snapshot is never
// older than its parent: when the clock is behind the parent's time (a
// clock set back, or another server's clock ahead), it gets the parent's
// time plus one microsecond.
func (s *Service) record(ctx context.Context, p ports.Principal, ws ports.Workspace, branch ports.Branch, head base, r record,
	complete completer) (CommitResult, error) {
	now := s.nowUs()
	if head.snap.ID != "" && now <= head.snap.CreatedAtUs {
		now = head.snap.CreatedAtUs + 1
	}
	parents := []string{}
	files, bytes := head.snap.EntryCount+r.delta.Files, head.snap.TotalBytes+r.delta.Bytes
	if head.snap.ID != "" {
		parents = append(parents, head.snap.ID)
	}
	if r.counts != nil {
		files, bytes = r.counts.Files, r.counts.Bytes
	}
	if files < 0 || bytes < 0 {
		return CommitResult{}, fmt.Errorf("core: snapshot counts %d files, %d bytes", files, bytes)
	}
	snap := ports.Snapshot{ID: s.d.IDs.NewID(), WorkspaceID: ws.ID, ProjectID: ws.ProjectID, BranchID: branch.ID,
		RefName: branch.RefName, AuthorRef: p.Ref, AuthorPrincipalUID: p.PrincipalUID, Message: r.message,
		TriggerKind: r.trigger, RootTreeHash: r.root, TreeFormatVersion: tree.FormatVersion, ParentIDs: parents,
		EntryCount: files, TotalBytes: bytes, CreatedAtUs: now}
	result := CommitResult{SnapshotID: snap.ID, BranchID: branch.ID, RefName: branch.RefName, Generation: branch.Generation + 1,
		RootTreeHash: r.root.String(), ParentIDs: parents, RestoredFrom: r.restoredFrom, CreatedAtUs: now}
	snapEvent, err := json.Marshal(snapshotEvent{SchemaVersion: 1, TenantID: ws.TenantID, ProjectID: ws.ProjectID,
		WorkspaceID: ws.ID, SnapshotID: snap.ID, BranchID: branch.ID, RefName: branch.RefName,
		RootTreeHash: r.root.String(), ParentIDs: parents, TriggerKind: r.trigger, RestoredFrom: r.restoredFrom,
		ActorRef: p.Ref, EntryCount: files, TotalBytes: bytes, CreatedAt: rfc3339(now)})
	if err != nil {
		return CommitResult{}, err
	}
	var prevHead *string
	if branch.HeadSnapshotID != "" {
		prevHead = &branch.HeadSnapshotID
	}
	branchEvent, err := json.Marshal(branchUpdatedEvent{SchemaVersion: 1, TenantID: ws.TenantID, ProjectID: ws.ProjectID,
		WorkspaceID: ws.ID, BranchID: branch.ID, RefName: branch.RefName, HeadSnapshotID: snap.ID,
		PreviousHeadSnapshotID: prevHead, Generation: branch.Generation + 1, ActorRef: p.Ref, UpdatedAt: rfc3339(now)})
	if err != nil {
		return CommitResult{}, err
	}
	errCAS := errors.New("generation changed")
	err = s.d.Store.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		ok, err := s.d.Store.UpdateBranchHead(ctx, tx, branch.ID, ws.ID, branch.Generation, snap.ID, now)
		if err != nil {
			return err
		}
		if !ok {
			return errCAS
		}
		if err := s.d.Store.InsertSnapshot(ctx, tx, snap); err != nil {
			return err
		}
		if len(r.live) > 0 {
			if err := s.d.Store.MarkBlobsLive(ctx, tx, ws.ID, r.live); err != nil {
				return err
			}
		}
		for _, ev := range []ports.Event{
			{ID: s.d.IDs.NewID(), Type: r.event, WorkspaceID: ws.ID, AggregateID: snap.ID, Payload: snapEvent, CreatedAtUs: now},
			{ID: s.d.IDs.NewID(), Type: ports.EventBranchUpdated, WorkspaceID: ws.ID, AggregateID: branch.ID, Payload: branchEvent, CreatedAtUs: now},
		} {
			if err := s.d.Store.Append(ctx, tx, ev); err != nil {
				return err
			}
		}
		return completeIf(ctx, complete, tx, result)
	})
	if errors.Is(err, errCAS) {
		current, gerr := s.d.Store.GetBranch(ctx, nil, branch.ID)
		if gerr != nil {
			return CommitResult{}, mapErr(gerr, "branch")
		}
		return CommitResult{}, generationConflict(current.Generation)
	}
	if err != nil {
		return CommitResult{}, mapErr(err, "snapshot")
	}
	return result, nil
}

func rfc3339(us int64) string { return time.UnixMicro(us).UTC().Format("2006-01-02T15:04:05.000000Z") }

// Restore creates a snapshot on a branch from another snapshot of the same
// workspace; its parent is the current head (history is never rewritten).
// Without a path the new tree is the target's tree, reused as is. With a
// path, the file or directory at that path in the target replaces the one
// at the same place in the head (the subtree is reused), or, if the target
// has nothing there, the path is removed from the head. The path is looked
// up with the lookup rules; restored entries keep their stored bytes. The
// transaction is that of a commit, with trigger_kind restore and the
// event snapshot.restored.
func (s *Service) Restore(ctx context.Context, p ports.Principal, snapshotID string, req RestoreRequest, idemKey string) (CommitResult, error) {
	if err := checkTarget(req.BranchID, req.ExpectedGeneration, req.Message); err != nil {
		return CommitResult{}, err
	}
	var nfc, raw string
	if req.Path != nil {
		var err error
		if nfc, raw, err = pathx.NormalizeLookup(*req.Path); err != nil {
			return CommitResult{}, err
		}
	}
	target, ws, err := s.snapshotAccess(ctx, p, snapshotID, ports.ActionWorkspaceWrite)
	if err != nil {
		return CommitResult{}, err
	}
	branch, err := s.branchOf(ctx, ws.ID, req.BranchID)
	if err != nil {
		return CommitResult{}, err
	}
	hashed := struct {
		SnapshotID string         `json:"snapshot_id"`
		Request    RestoreRequest `json:"request"`
	}{snapshotID, req}
	return idempotent(ctx, s, p, RouteRestore, idemKey, hashed, func(ctx context.Context, complete completer) (CommitResult, error) {
		if branch.Generation != req.ExpectedGeneration {
			return CommitResult{}, generationConflict(branch.Generation)
		}
		head, err := s.headOf(ctx, branch)
		if err != nil {
			return CommitResult{}, err
		}
		r := record{trigger: ports.TriggerRestore, message: req.Message, restoredFrom: target.ID, event: ports.EventSnapshotRestored}
		if req.Path == nil {
			r.root = target.RootTreeHash
			r.counts = &tree.Stats{Files: target.EntryCount, Bytes: target.TotalBytes}
		} else {
			g := s.getter(ws.ID)
			var res tree.Result
			e, segs, err := tree.LookupPath(ctx, g, target.RootTreeHash, raw, nfc)
			switch {
			case err == nil:
				res, err = tree.Graft(ctx, g, head.root, segs, &e)
			case errors.Is(err, tree.ErrPathNotFound):
				var hsegs []string
				if _, hsegs, err = tree.LookupPath(ctx, g, head.root, raw, nfc); err == nil {
					res, err = tree.Graft(ctx, g, head.root, hsegs, nil)
				} else if errors.Is(err, tree.ErrPathNotFound) {
					return CommitResult{}, invalidReason(ReasonNoChanges, "the path exists neither in the snapshot nor in the branch head")
				}
			}
			if err != nil {
				return CommitResult{}, treeError(err)
			}
			if err := s.putNodes(ctx, ws.ID, res.Nodes); err != nil {
				return CommitResult{}, err
			}
			r.root, r.delta = res.Root, res.Delta
		}
		if r.root == head.root {
			return CommitResult{}, invalidReason(ReasonNoChanges, "the branch head already has this content")
		}
		return s.record(ctx, p, ws, branch, head, r, complete)
	})
}
