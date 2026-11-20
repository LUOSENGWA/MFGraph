package tree

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/pathx"
)

// Resource limits of the functions that read stored trees on behalf of a
// request (Lookup, ListDir, Diff, Apply, Graft, Count). They read only the
// nodes they need and verify every node they read (DecodeVerified); unlike
// Walk they do not check that a whole directory is paged canonically, so
// they never load a whole paged directory just to check it.
const (
	// MaxDepth bounds the directory depth: a path has at most
	// pathx.MaxPathBytes bytes, hence at most 2048 segments.
	MaxDepth = (pathx.MaxPathBytes + 1) / 2
	// maxIndexDepth bounds the dir_index levels of one directory. Format 1
	// needs 3 levels for 2^30 entries and 6 for more entries than a size
	// field can count.
	maxIndexDepth      = 8
	maxCachedNodes     = 64
	maxCachedNodeBytes = 8 << 20
)

var (
	// ErrPathNotFound: no entry exists at the path (or a segment above it is
	// a file).
	ErrPathNotFound = errors.New("tree: path not found")
	// ErrTooDeep: a tree is nested deeper than MaxDepth directories or
	// maxIndexDepth index levels.
	ErrTooDeep = errors.New("tree: tree too deep")
)

// EmptyRoot is the hash of the empty root directory
// {"entries":[],"format":1,"kind":"dir"}. Readers know its node without
// fetching it, so it need not be stored.
var EmptyRoot = HashOf(Encode(Node{Kind: KindDir}))

// reader fetches and verifies nodes, caching the decoded nodes of one
// operation.
type reader struct {
	ctx   context.Context
	g     Getter
	cache map[contenthash.Hash]Node
	order []cachedNode
	bytes int
	// Apply/Graft retain only the hashes after eviction, to omit base nodes
	// from Result.Nodes. Other reads need no history.
	baseHashes map[contenthash.Hash]struct{}
}

type cachedNode struct {
	hash contenthash.Hash
	size int
}

func newReader(ctx context.Context, g Getter) *reader {
	return &reader{ctx: ctx, g: g, cache: map[contenthash.Hash]Node{}}
}

func (r *reader) node(h contenthash.Hash) (Node, error) {
	if n, ok := r.cache[h]; ok {
		return n, nil
	}
	if h == EmptyRoot {
		return Node{Kind: KindDir, Entries: []Entry{}}, nil
	}
	if err := r.ctx.Err(); err != nil {
		return Node{}, err
	}
	b, err := r.g.GetNode(r.ctx, h)
	if err != nil {
		return Node{}, err
	}
	n, err := DecodeVerified(b, h)
	if err != nil {
		return Node{}, err
	}
	for len(r.cache) >= maxCachedNodes || r.bytes+len(b) > maxCachedNodeBytes {
		oldest := r.order[0]
		delete(r.cache, oldest.hash)
		r.bytes -= oldest.size
		r.order = r.order[1:]
	}
	r.order = append(r.order, cachedNode{h, len(b)})
	r.bytes += len(b)
	r.cache[h] = n
	if r.baseHashes != nil {
		r.baseHashes[h] = struct{}{}
	}
	return n, nil
}

// top returns the top node of a directory: a dir or a dir_index node.
func (r *reader) top(h contenthash.Hash) (Node, error) {
	n, err := r.node(h)
	if err != nil {
		return Node{}, err
	}
	if n.Kind == KindDirPage {
		return Node{}, fmt.Errorf("%w: a directory points to the dir_page node %s", ErrInvalidTree, h)
	}
	return n, nil
}

// child returns a node referenced by a dir_index: a dir_page or a dir_index.
func (r *reader) child(h contenthash.Hash) (Node, error) {
	n, err := r.node(h)
	if err != nil {
		return Node{}, err
	}
	if n.Kind == KindDir {
		return Node{}, fmt.Errorf("%w: an index references the dir node %s", ErrInvalidTree, h)
	}
	return n, nil
}

// refFor returns the index of the reference whose subtree may contain name:
// the last reference whose first name is <= name, or 0.
func refFor(refs []PageRef, name string) int {
	i := sort.Search(len(refs), func(i int) bool { return refs[i].First > name })
	if i > 0 {
		i--
	}
	return i
}

// find looks up name in the directory whose top node is dir, reading the
// dir node, or the index nodes on the way down and one page.
func (r *reader) find(dir contenthash.Hash, name string) (Entry, bool, error) {
	n, err := r.top(dir)
	if err != nil {
		return Entry{}, false, err
	}
	for level := 0; n.Kind == KindDirIndex; level++ {
		if level >= maxIndexDepth {
			return Entry{}, false, fmt.Errorf("%w: more than %d index levels", ErrTooDeep, maxIndexDepth)
		}
		if n, err = r.child(n.Pages[refFor(n.Pages, name)].Hash); err != nil {
			return Entry{}, false, err
		}
	}
	i := sort.Search(len(n.Entries), func(i int) bool { return n.Entries[i].Name >= name })
	if i < len(n.Entries) && n.Entries[i].Name == name {
		return n.Entries[i], true, nil
	}
	return Entry{}, false, nil
}

// Lookup returns the entry at the stored path segs below root, comparing
// each segment byte for byte with the stored names. With no segments it
// returns the root directory (Name "", Type TypeDir, Hash root). It reads
// one node per directory level plus the index levels of paged directories.
// A missing segment, or a segment below a file, gives ErrPathNotFound.
func Lookup(ctx context.Context, g Getter, root contenthash.Hash, segs []string) (Entry, error) {
	e, _, err := lookup(newReader(ctx, g), root, segs)
	return e, err
}

// lookup also returns the hashes of the directories on the way: dirs[i] is
// the directory that contains segs[i] (dirs[0] = root).
func lookup(r *reader, root contenthash.Hash, segs []string) (Entry, []contenthash.Hash, error) {
	if len(segs) > MaxDepth {
		return Entry{}, nil, fmt.Errorf("%w: %d segments", ErrTooDeep, len(segs))
	}
	cur := Entry{Type: TypeDir, Hash: root}
	dirs := make([]contenthash.Hash, 0, len(segs))
	for i, s := range segs {
		if cur.Type != TypeDir {
			return Entry{}, nil, fmt.Errorf("%w: %q is a file", ErrPathNotFound, strings.Join(segs[:i], "/"))
		}
		dirs = append(dirs, cur.Hash)
		e, ok, err := r.find(cur.Hash, s)
		if err != nil {
			return Entry{}, nil, err
		}
		if !ok {
			return Entry{}, nil, fmt.Errorf("%w: %q", ErrPathNotFound, strings.Join(segs[:i+1], "/"))
		}
		cur = e
	}
	return cur, dirs, nil
}

// split splits a path into segments; "" is the root (no segments).
func split(p string) []string {
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// LookupPath finds an existing entry with the lookup rules of the
// specification (section 2.6, step 4): raw and nfc are the two results of
// pathx.NormalizeLookup. The entry whose path is raw byte for byte is found
// first; if there is none, the entry whose path is nfc. It returns the entry
// and its stored path (segments). Without either, ErrPathNotFound.
func LookupPath(ctx context.Context, g Getter, root contenthash.Hash, raw, nfc string) (Entry, []string, error) {
	r := newReader(ctx, g)
	segs := split(raw)
	e, _, err := lookup(r, root, segs)
	if errors.Is(err, ErrPathNotFound) && nfc != raw {
		segs = split(nfc)
		e, _, err = lookup(r, root, segs)
	}
	if err != nil {
		return Entry{}, nil, err
	}
	return e, segs, nil
}

// dirIter iterates over the entries of one directory in name order. In a
// paged directory it reads a page only when an entry of it is needed: an
// iterator positioned at the start of a reference (refStart) can skip the
// whole referenced page or index without reading it (skipRef).
type dirIter struct {
	r      *reader
	frames []iterFrame // the dir_index levels; the last frame's refs[i] is the current reference
	leaf   []Entry     // entries of the dir node, or of the loaded page
	pos    int
	loaded bool // leaf holds the current reference's entries (always true for a dir node)
	done   bool
}

type iterFrame struct {
	refs []PageRef
	i    int
}

// openDir positions an iterator at the first entry of the directory dir
// whose name is >= from ("" = the first entry). A zero dir is an absent
// directory without entries.
func openDir(r *reader, dir contenthash.Hash, from string) (*dirIter, error) {
	it := &dirIter{r: r}
	if dir.IsZero() {
		it.done = true
		return it, nil
	}
	n, err := r.top(dir)
	if err != nil {
		return nil, err
	}
	if n.Kind == KindDir {
		it.leaf, it.loaded = n.Entries, true
		it.pos = sort.Search(len(it.leaf), func(i int) bool { return it.leaf[i].Name >= from })
		return it, it.settle()
	}
	it.frames = []iterFrame{{refs: n.Pages, i: refFor(n.Pages, from)}}
	if from == "" {
		return it, nil // positioned at the start of the first reference, nothing read yet
	}
	if err := it.load(from); err != nil {
		return nil, err
	}
	it.pos = sort.Search(len(it.leaf), func(i int) bool { return it.leaf[i].Name >= from })
	return it, it.settle()
}

// load reads the current reference down to a page, choosing at each lower
// index level the reference that may contain from ("" = the first one).
func (it *dirIter) load(from string) error {
	for {
		f := it.frames[len(it.frames)-1]
		n, err := it.r.child(f.refs[f.i].Hash)
		if err != nil {
			return err
		}
		if n.Kind == KindDirPage {
			it.leaf, it.pos, it.loaded = n.Entries, 0, true
			return nil
		}
		if len(it.frames) >= maxIndexDepth {
			return fmt.Errorf("%w: more than %d index levels", ErrTooDeep, maxIndexDepth)
		}
		it.frames = append(it.frames, iterFrame{refs: n.Pages, i: refFor(n.Pages, from)})
	}
}

// nextRef moves to the start of the next reference, without reading it.
func (it *dirIter) nextRef() {
	it.loaded, it.leaf, it.pos = false, nil, 0
	for len(it.frames) > 0 {
		f := &it.frames[len(it.frames)-1]
		f.i++
		if f.i < len(f.refs) {
			return
		}
		it.frames = it.frames[:len(it.frames)-1]
	}
	it.done = true
}

// settle moves past the end of an exhausted page (or dir node).
func (it *dirIter) settle() error {
	if it.loaded && it.pos >= len(it.leaf) {
		if len(it.frames) == 0 {
			it.done = true
			return nil
		}
		it.nextRef()
	}
	return nil
}

func (it *dirIter) peek() (Entry, bool, error) {
	for !it.done {
		if !it.loaded {
			if err := it.load(""); err != nil {
				return Entry{}, false, err
			}
		}
		if it.pos < len(it.leaf) {
			return it.leaf[it.pos], true, nil
		}
		if err := it.settle(); err != nil {
			return Entry{}, false, err
		}
	}
	return Entry{}, false, nil
}

func (it *dirIter) next() error {
	it.pos++
	return it.settle()
}

// refStart reports the hash of the current reference while the iterator is
// at its start and has not read it.
func (it *dirIter) refStart() (contenthash.Hash, bool) {
	if it.done || it.loaded || len(it.frames) == 0 {
		return contenthash.Hash{}, false
	}
	f := it.frames[len(it.frames)-1]
	return f.refs[f.i].Hash, true
}

// skipRef moves past the current reference without reading it.
func (it *dirIter) skipRef() { it.nextRef() }

// ListDir lists up to limit entries of the directory whose top node is dir
// (a dir or a top-level dir_index node), in name order, starting after the
// name after ("" = from the first entry). In a paged directory it finds the
// page of the first entry by binary search on the first names of the index
// references and then reads the following pages one at a time, so a page
// of a listing reads at most two pages per index level. more reports
// whether more entries follow.
func ListDir(ctx context.Context, g Getter, dir contenthash.Hash, after string, limit int) (entries []Entry, more bool, err error) {
	if limit < 1 {
		return nil, false, fmt.Errorf("tree: ListDir limit %d", limit)
	}
	it, err := openDir(newReader(ctx, g), dir, after)
	if err != nil {
		return nil, false, err
	}
	for {
		e, ok, err := it.peek()
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return entries, false, nil
		}
		if after == "" || e.Name > after {
			if len(entries) == limit {
				return entries, true, nil
			}
			entries = append(entries, e)
		}
		if err := it.next(); err != nil {
			return nil, false, err
		}
	}
}

// Count returns the number of files and the sum of their sizes below the
// directory dir. It reads every node of the subtree (each one verified).
func Count(ctx context.Context, g Getter, dir contenthash.Hash) (files, bytes int64, err error) {
	return count(newReader(ctx, g), dir, 0)
}

func count(r *reader, dir contenthash.Hash, depth int) (files, bytes int64, err error) {
	if depth > MaxDepth {
		return 0, 0, fmt.Errorf("%w: more than %d levels", ErrTooDeep, MaxDepth)
	}
	it, err := openDir(r, dir, "")
	if err != nil {
		return 0, 0, err
	}
	for {
		e, ok, err := it.peek()
		if err != nil {
			return 0, 0, err
		}
		if !ok {
			return files, bytes, nil
		}
		if e.Type == TypeFile {
			files++
			bytes += e.Size
		} else {
			f, b, err := count(r, e.Hash, depth+1)
			if err != nil {
				return 0, 0, err
			}
			files += f
			bytes += b
		}
		if err := it.next(); err != nil {
			return 0, 0, err
		}
	}
}
