package tree

import (
	"context"
	"fmt"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/pathx"
)

// Getter returns the canonical bytes of the node with hash h. Implementations
// need not verify the bytes; the walker always does.
type Getter interface {
	GetNode(ctx context.Context, h contenthash.Hash) ([]byte, error)
}

// GetterFunc adapts a function to Getter.
type GetterFunc func(ctx context.Context, h contenthash.Hash) ([]byte, error)

// GetNode calls f.
func (f GetterFunc) GetNode(ctx context.Context, h contenthash.Hash) ([]byte, error) {
	return f(ctx, h)
}

// MapGetter serves nodes from a map, such as the one returned by Build.
type MapGetter map[contenthash.Hash][]byte

// GetNode returns m[h] or ErrNodeNotFound.
func (m MapGetter) GetNode(_ context.Context, h contenthash.Hash) ([]byte, error) {
	b, ok := m[h]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNodeNotFound, h)
	}
	return b, nil
}

// NodeInfo describes one node visited by WalkNodes.
type NodeInfo struct {
	Dir       string // directory the node belongs to ("" for the root)
	Hash      contenthash.Hash
	Node      Node
	Canonical []byte
}

// WalkFunc is called for every entry, with its full path. Directory entries
// are reported before their contents.
type WalkFunc func(path string, e Entry) error

// Walk visits every file and directory below root in pre-order, siblings in
// byte order of their names. It verifies the whole tree on the way: node
// hashes, strict decoding, no empty subdirectories, page reference counts and
// first names, global ordering and sibling conflicts across pages, and that
// every paged directory is split exactly as Build would split it.
func Walk(ctx context.Context, g Getter, root contenthash.Hash, fn WalkFunc) error {
	w := walker{ctx: ctx, g: g, lim: frozenLimits, onEntry: fn}
	return w.dir("", root, true)
}

// WalkNodes visits every node in walk order: a directory's own nodes (its
// dir node, or its top dir_index followed by its pages in order) and then its
// subdirectories by name. A subtree shared by several directories is visited
// once per directory. It performs the same verification as Walk.
func WalkNodes(ctx context.Context, g Getter, root contenthash.Hash, fn func(NodeInfo) error) error {
	w := walker{ctx: ctx, g: g, lim: frozenLimits, onNode: fn}
	return w.dir("", root, true)
}

type walker struct {
	ctx     context.Context
	g       Getter
	lim     limits
	onNode  func(NodeInfo) error
	onEntry WalkFunc
}

func (w *walker) fetch(dir string, h contenthash.Hash) (NodeInfo, error) {
	if err := w.ctx.Err(); err != nil {
		return NodeInfo{}, err
	}
	b, err := w.g.GetNode(w.ctx, h)
	if err != nil {
		return NodeInfo{}, err
	}
	n, err := DecodeVerified(b, h)
	if err != nil {
		return NodeInfo{}, fmt.Errorf("directory %q: %w", dir, err)
	}
	return NodeInfo{Dir: dir, Hash: h, Node: n, Canonical: b}, nil
}

func (w *walker) dir(path string, ref contenthash.Hash, isRoot bool) error {
	entries, infos, err := w.loadDir(path, ref, isRoot)
	if err != nil {
		return err
	}
	if w.onNode != nil {
		for _, ni := range infos {
			if err := w.onNode(ni); err != nil {
				return err
			}
		}
	}
	for _, e := range entries {
		full := joinPath(path, e.Name)
		if w.onEntry != nil {
			if err := w.onEntry(full, e); err != nil {
				return err
			}
		}
		if e.Type == TypeDir {
			if err := w.dir(full, e.Hash, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *walker) loadDir(path string, ref contenthash.Hash, isRoot bool) ([]Entry, []NodeInfo, error) {
	top, err := w.fetch(path, ref)
	if err != nil {
		return nil, nil, err
	}
	infos := []NodeInfo{top}
	switch top.Node.Kind {
	case KindDir:
		if !isRoot && len(top.Node.Entries) == 0 {
			return nil, nil, fmt.Errorf("%w: directory %q is empty (only the root may be empty)", ErrInvalidTree, path)
		}
		return top.Node.Entries, infos, nil
	case KindDirPage:
		return nil, nil, fmt.Errorf("%w: directory %q points to a dir_page node", ErrInvalidTree, path)
	}
	entries, err := w.expand(path, top.Node, &infos)
	if err != nil {
		return nil, nil, err
	}
	sib := make([]pathx.Sibling, len(entries))
	for i, e := range entries {
		if i > 0 && entries[i-1].Name >= e.Name {
			return nil, nil, fmt.Errorf("%w: directory %q: entries out of order across pages", ErrInvalidTree, path)
		}
		sib[i] = pathx.Sibling{Name: e.Name, Dir: e.Type == TypeDir}
	}
	if err := pathx.CheckSiblings(sib); err != nil {
		return nil, nil, fmt.Errorf("%w: directory %q: %v", ErrInvalidTree, path, err)
	}
	// The paging must be exactly the canonical one.
	h, err := layout(entries, w.lim, func(contenthash.Hash, []byte) {})
	if err != nil {
		return nil, nil, err
	}
	if h != ref {
		return nil, nil, fmt.Errorf("%w: directory %q is not split canonically", ErrInvalidTree, path)
	}
	return entries, infos, nil
}

func (w *walker) expand(path string, idx Node, infos *[]NodeInfo) ([]Entry, error) {
	var all []Entry
	for _, p := range idx.Pages {
		child, err := w.fetch(path, p.Hash)
		if err != nil {
			return nil, err
		}
		*infos = append(*infos, child)
		var sub []Entry
		switch child.Node.Kind {
		case KindDirPage:
			sub = child.Node.Entries
		case KindDirIndex:
			if sub, err = w.expand(path, child.Node, infos); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("%w: directory %q: index references a %s node", ErrInvalidTree, path, child.Node.Kind)
		}
		if int64(len(sub)) != p.Count || sub[0].Name != p.First {
			return nil, fmt.Errorf("%w: directory %q: reference count/first does not match the referenced node", ErrInvalidTree, path)
		}
		all = append(all, sub...)
	}
	return all, nil
}
