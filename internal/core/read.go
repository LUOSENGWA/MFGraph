package core

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/pathx"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/storagekey"
	"github.com/LUOSENGWA/MFGraph/internal/tree"
)

// GetSnapshot returns a snapshot the caller can see.
func (s *Service) GetSnapshot(ctx context.Context, p ports.Principal, id string) (ports.Snapshot, error) {
	snap, _, err := s.snapshotAccess(ctx, p, id, ports.ActionWorkspaceRead)
	return snap, err
}

// Timeline lists the snapshots of a workspace, newest first; with branchID
// only those created on that branch.
func (s *Service) Timeline(ctx context.Context, p ports.Principal, workspaceID, branchID string, pg Page) ([]ports.Snapshot, string, error) {
	if err := checkID("workspace_id", workspaceID); err != nil {
		return nil, "", err
	}
	limit, err := pg.limit()
	if err != nil {
		return nil, "", err
	}
	q := ports.SnapshotQuery{WorkspaceID: workspaceID, Limit: limit + 1}
	if pg.Cursor != "" {
		if q.Before, err = parseTimelineCursor(pg.Cursor); err != nil {
			return nil, "", err
		}
	}
	if branchID != "" {
		if err := checkID("branch_id", branchID); err != nil {
			return nil, "", err
		}
	}
	if _, _, err := s.access(ctx, p, workspaceID, ports.ActionWorkspaceRead); err != nil {
		return nil, "", err
	}
	if branchID != "" {
		if _, err := s.branchOf(ctx, workspaceID, branchID); err != nil {
			return nil, "", err
		}
		q.BranchID = branchID
	}
	snaps, err := s.d.Store.ListSnapshots(ctx, nil, q)
	if err != nil {
		return nil, "", mapErr(err, "snapshots")
	}
	next := ""
	if len(snaps) > limit {
		snaps = snaps[:limit]
		last := snaps[limit-1]
		next = timelineCursor(ports.TimelineCursor{CreatedAtUs: last.CreatedAtUs, SnapshotID: last.ID})
	}
	return snaps, next, nil
}

// TreeEntry is one entry of a directory listing, with its stored path.
type TreeEntry struct {
	Path  string
	Entry tree.Entry
}

// TreeListing is one page of a directory.
type TreeListing struct {
	SnapshotID string
	Path       string // stored path of the directory ("" = root)
	Items      []TreeEntry
	NextCursor string
}

// ListTree lists one page of the directory dir of a snapshot ("" = the
// root), in byte order of the names. dir is looked up with the lookup
// rules; a paged directory is not read as a whole.
func (s *Service) ListTree(ctx context.Context, p ports.Principal, snapshotID, dir string, pg Page) (TreeListing, error) {
	if err := checkID("snapshot_id", snapshotID); err != nil {
		return TreeListing{}, err
	}
	limit, err := pg.limit()
	if err != nil {
		return TreeListing{}, err
	}
	after := ""
	if pg.Cursor != "" {
		if after, err = decodeCursor(cursorTree, pg.Cursor); err != nil {
			return TreeListing{}, err
		}
	}
	var nfc, raw string
	if dir != "" {
		if nfc, raw, err = pathx.NormalizeLookup(dir); err != nil {
			return TreeListing{}, err
		}
	}
	snap, ws, err := s.snapshotAccess(ctx, p, snapshotID, ports.ActionWorkspaceRead)
	if err != nil {
		return TreeListing{}, err
	}
	g := s.getter(ws.ID)
	e, segs := tree.Entry{Type: tree.TypeDir, Hash: snap.RootTreeHash}, []string(nil)
	if dir != "" {
		if e, segs, err = tree.LookupPath(ctx, g, snap.RootTreeHash, raw, nfc); err != nil {
			if errors.Is(err, tree.ErrPathNotFound) {
				return TreeListing{}, notFound("path")
			}
			return TreeListing{}, mapErr(err, "tree node")
		}
		if e.Type != tree.TypeDir {
			return TreeListing{}, invalidReason(ReasonNotADirectory, "the path names a file")
		}
	}
	entries, more, err := tree.ListDir(ctx, g, e.Hash, after, limit)
	if err != nil {
		return TreeListing{}, mapErr(err, "tree node")
	}
	stored := strings.Join(segs, "/")
	out := TreeListing{SnapshotID: snap.ID, Path: stored, Items: make([]TreeEntry, len(entries))}
	for i, x := range entries {
		out.Items[i] = TreeEntry{Path: join(stored, x.Name), Entry: x}
	}
	if more {
		out.NextCursor = encodeCursor(cursorTree, entries[len(entries)-1].Name)
	}
	return out, nil
}

func join(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// File is a file of a snapshot, ready to be read.
type File struct {
	Path  string // stored path
	Entry tree.Entry
	ws    ports.Workspace
	s     *Service
}

// ETag is the quoted content hash.
func (f *File) ETag() string { return `"` + f.Entry.Hash.String() + `"` }

// Open reads length bytes from offset (length < 0: to the end). It first
// checks the stored object, before the caller sends any header: content
// that is no longer in storage, or whose size is not the size of the file
// (a truncated or damaged object, which is also logged), gives
// blob_missing (missing_in_storage).
func (f *File) Open(ctx context.Context, offset, length int64) (io.ReadCloser, error) {
	key := storagekey.Join(f.ws.StoragePrefix, storagekey.ObjectKey(f.Entry.Hash))
	missing := func() error {
		return blobMissing(ReasonMissingInStorage, "the content of the file is missing from storage or damaged",
			[]string{f.Entry.Hash.String()})
	}
	info, err := f.s.d.Objects.Stat(ctx, key)
	switch {
	case errors.Is(err, ports.ErrNotFound):
		return nil, missing()
	case err != nil:
		return nil, mapErr(err, "object")
	case info.Size != f.Entry.Size:
		f.s.d.Logger.Warn("a stored object does not have the size of its file", "workspace_id", f.ws.ID,
			"hash", f.Entry.Hash.String(), "size", info.Size, "expected_size", f.Entry.Size)
		return nil, missing()
	}
	rc, err := f.s.d.Objects.Get(ctx, key, offset, length)
	if errors.Is(err, ports.ErrNotFound) {
		return nil, missing()
	}
	return rc, mapErr(err, "object")
}

// GetFile finds a file of a snapshot with the lookup rules.
func (s *Service) GetFile(ctx context.Context, p ports.Principal, snapshotID, path string) (*File, error) {
	if err := checkID("snapshot_id", snapshotID); err != nil {
		return nil, err
	}
	nfc, raw, err := pathx.NormalizeLookup(path)
	if err != nil {
		return nil, err
	}
	snap, ws, err := s.snapshotAccess(ctx, p, snapshotID, ports.ActionWorkspaceRead)
	if err != nil {
		return nil, err
	}
	e, segs, err := tree.LookupPath(ctx, s.getter(ws.ID), snap.RootTreeHash, raw, nfc)
	if err != nil {
		if errors.Is(err, tree.ErrPathNotFound) {
			return nil, notFound("path")
		}
		return nil, mapErr(err, "tree node")
	}
	if e.Type != tree.TypeFile {
		return nil, invalidReason(ReasonNotAFile, "the path names a directory")
	}
	return &File{Path: strings.Join(segs, "/"), Entry: e, ws: ws, s: s}, nil
}

// DiffResult is one page of a diff.
type DiffResult struct {
	BaseSnapshotID   string // "" = the empty tree
	TargetSnapshotID string
	Items            []tree.DiffEntry
	NextCursor       string
}

// Diff lists the files that differ from base to the snapshot, in path
// order. Without base the first parent is the base (the empty tree for a
// snapshot without parents); base must be a snapshot of the same
// workspace.
func (s *Service) Diff(ctx context.Context, p ports.Principal, snapshotID, baseID string, pg Page) (DiffResult, error) {
	if err := checkID("snapshot_id", snapshotID); err != nil {
		return DiffResult{}, err
	}
	if baseID != "" {
		if err := checkID("base", baseID); err != nil {
			return DiffResult{}, err
		}
	}
	limit, err := pg.limit()
	if err != nil {
		return DiffResult{}, err
	}
	var after []string
	if pg.Cursor != "" {
		pos, err := decodeCursor(cursorDiff, pg.Cursor)
		if err != nil {
			return DiffResult{}, err
		}
		// The position is a stored path: 1 to tree.MaxDepth non-empty
		// segments.
		after = strings.Split(pos, "/")
		if len(after) > tree.MaxDepth || slices.Contains(after, "") {
			return DiffResult{}, invalidCursor()
		}
	}
	target, ws, err := s.snapshotAccess(ctx, p, snapshotID, ports.ActionWorkspaceRead)
	if err != nil {
		return DiffResult{}, err
	}
	if baseID == "" && len(target.ParentIDs) > 0 {
		baseID = target.ParentIDs[0]
	}
	var baseRoot contenthash.Hash
	if baseID != "" {
		b, err := s.d.Store.GetSnapshot(ctx, nil, baseID)
		if err != nil {
			return DiffResult{}, mapErr(err, "base snapshot")
		}
		if b.WorkspaceID != ws.ID {
			return DiffResult{}, notFound("base snapshot")
		}
		baseRoot = b.RootTreeHash
	}
	items, more, err := tree.Diff(ctx, s.getter(ws.ID), baseRoot, target.RootTreeHash, after, limit)
	if err != nil {
		return DiffResult{}, mapErr(err, "tree node")
	}
	out := DiffResult{BaseSnapshotID: baseID, TargetSnapshotID: target.ID, Items: items}
	if more {
		out.NextCursor = encodeCursor(cursorDiff, items[len(items)-1].Path)
	}
	return out, nil
}
