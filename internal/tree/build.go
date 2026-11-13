package tree

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/pathx"
)

// FileEntry is one input file of Build: its full path and content identity.
type FileEntry struct {
	Path string // '/'-separated relative path; normalized by Build
	Hash contenthash.Hash
	Size int64
	Mode uint32 // ModeRegular or ModeExecutable
}

// ModeFromPerm maps local permission bits to a tree mode: any execute bit
// gives ModeExecutable, otherwise ModeRegular.
func ModeFromPerm(perm fs.FileMode) uint32 {
	if perm&0o111 != 0 {
		return ModeExecutable
	}
	return ModeRegular
}

// limits are the splitting thresholds. Only frozenLimits is used outside
// tests; smaller limits exercise the splitting code paths in unit tests.
type limits struct {
	maxItems int
	maxBytes int
}

var frozenLimits = limits{maxItems: MaxEntriesPerNode, maxBytes: MaxNodeBytes}

// Build constructs the whole tree for a set of files and returns the root
// hash and every node by hash (canonical, uncompressed bytes).
//
// Every path is normalized with pathx.Normalize; duplicate paths (including
// paths that only differ in Unicode normalization), a name used both as file
// and directory, and case-fold conflicts between siblings are rejected with a
// *pathx.Error. Directories are implicit; an empty input gives the empty
// root directory. The result does not depend on the order of files.
func Build(files []FileEntry) (contenthash.Hash, map[contenthash.Hash][]byte, error) {
	return build(files, frozenLimits)
}

type dirBuilder struct {
	files map[string]FileEntry
	dirs  map[string]*dirBuilder
}

func newDirBuilder() *dirBuilder {
	return &dirBuilder{files: map[string]FileEntry{}, dirs: map[string]*dirBuilder{}}
}

func build(files []FileEntry, lim limits) (contenthash.Hash, map[contenthash.Hash][]byte, error) {
	norm := make([]FileEntry, len(files))
	for i, f := range files {
		p, err := pathx.Normalize(f.Path)
		if err != nil {
			return contenthash.Hash{}, nil, err
		}
		if f.Hash.IsZero() {
			return contenthash.Hash{}, nil, fmt.Errorf("%w: %q has no content hash", ErrInvalidEntry, p)
		}
		if f.Size < 0 || f.Size > MaxSafeInteger {
			return contenthash.Hash{}, nil, fmt.Errorf("%w: %q: size %d out of range", ErrInvalidEntry, p, f.Size)
		}
		if f.Mode != ModeRegular && f.Mode != ModeExecutable {
			return contenthash.Hash{}, nil, fmt.Errorf("%w: %q: mode %d is not 420 or 493", ErrInvalidEntry, p, f.Mode)
		}
		f.Path = p
		norm[i] = f
	}
	// Sorting first makes the reported conflict independent of input order.
	sort.Slice(norm, func(i, j int) bool { return norm[i].Path < norm[j].Path })

	root := newDirBuilder()
	for _, f := range norm {
		if err := root.insert(f); err != nil {
			return contenthash.Hash{}, nil, err
		}
	}
	if err := root.checkSiblings(""); err != nil {
		return contenthash.Hash{}, nil, err
	}
	nodes := make(map[contenthash.Hash][]byte)
	h, err := root.encode(lim, func(h contenthash.Hash, b []byte) { nodes[h] = b })
	if err != nil {
		return contenthash.Hash{}, nil, err
	}
	return h, nodes, nil
}

func (d *dirBuilder) insert(f FileEntry) error {
	segs := strings.Split(f.Path, "/")
	cur := d
	for i, s := range segs[:len(segs)-1] {
		if _, isFile := cur.files[s]; isFile {
			return &pathx.Error{Kind: pathx.KindFileDirConflict, Input: strings.Join(segs[:i+1], "/"),
				Detail: "used as both a file and a directory"}
		}
		next := cur.dirs[s]
		if next == nil {
			next = newDirBuilder()
			cur.dirs[s] = next
		}
		cur = next
	}
	name := segs[len(segs)-1]
	if _, isDir := cur.dirs[name]; isDir {
		return &pathx.Error{Kind: pathx.KindFileDirConflict, Input: f.Path, Detail: "used as both a file and a directory"}
	}
	if _, dup := cur.files[name]; dup {
		return &pathx.Error{Kind: pathx.KindDuplicate, Input: f.Path, Detail: "path given more than once"}
	}
	cur.files[name] = f
	return nil
}

func (d *dirBuilder) names() []string {
	names := make([]string, 0, len(d.files)+len(d.dirs))
	for n := range d.files {
		names = append(names, n)
	}
	for n := range d.dirs {
		names = append(names, n)
	}
	sort.Strings(names) // byte order
	return names
}

func (d *dirBuilder) checkSiblings(dir string) error {
	names := d.names()
	sib := make([]pathx.Sibling, len(names))
	for i, n := range names {
		_, isDir := d.dirs[n]
		sib[i] = pathx.Sibling{Name: n, Dir: isDir}
	}
	if err := pathx.CheckSiblings(sib); err != nil {
		if pe, ok := err.(*pathx.Error); ok {
			return &pathx.Error{Kind: pe.Kind, Input: joinPath(dir, pe.Input), Detail: pe.Detail}
		}
		return err
	}
	for _, n := range names {
		if sub, ok := d.dirs[n]; ok {
			if err := sub.checkSiblings(joinPath(dir, n)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *dirBuilder) encode(lim limits, emit func(contenthash.Hash, []byte)) (contenthash.Hash, error) {
	names := d.names()
	entries := make([]Entry, 0, len(names))
	for _, n := range names {
		if f, ok := d.files[n]; ok {
			entries = append(entries, Entry{Name: n, Type: TypeFile, Hash: f.Hash, Size: f.Size, Mode: f.Mode})
			continue
		}
		h, err := d.dirs[n].encode(lim, emit)
		if err != nil {
			return contenthash.Hash{}, err
		}
		entries = append(entries, Entry{Name: n, Type: TypeDir, Hash: h})
	}
	return layout(entries, lim, emit)
}

func joinPath(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// span is a half-open index range [start, end).
type span struct{ start, end int }

// Encoded sizes of nodes without items; a node with n items encodes to
// overhead + sum(item sizes) + (n-1) commas.
var (
	overheadDir      = len(Encode(Node{Kind: KindDir}))
	overheadDirPage  = len(Encode(Node{Kind: KindDirPage}))
	overheadDirIndex = len(Encode(Node{Kind: KindDirIndex}))
)

func nodeSize(overhead int, sizes []int) int {
	n := overhead
	for i, s := range sizes {
		if i > 0 {
			n++
		}
		n += s
	}
	return n
}

// greedy packs items, in order, into groups: a group is closed when one more
// item would exceed lim.maxItems items or lim.maxBytes encoded bytes.
func greedy(sizes []int, overhead int, lim limits) ([]span, error) {
	var out []span
	start, size := 0, overhead
	for i, s := range sizes {
		add := s
		if i > start {
			add++ // comma
		}
		if i > start && (i-start+1 > lim.maxItems || size+add > lim.maxBytes) {
			out = append(out, span{start, i})
			start, size, add = i, overhead, s
		}
		if size+add > lim.maxBytes {
			return nil, fmt.Errorf("%w: a single item of %d bytes does not fit into a node", ErrInvalidEntry, s)
		}
		size += add
	}
	return append(out, span{start, len(sizes)}), nil
}

// layout emits the nodes of one directory and returns its reference hash.
//
//   - <= lim.maxItems entries and a "dir" encoding <= lim.maxBytes: one dir node;
//   - otherwise entries are packed greedily into dir_page nodes, and the pages
//     are referenced by dir_index nodes, packed by the same greedy rule level by
//     level until a single top-level dir_index fits.
func layout(entries []Entry, lim limits, emit func(contenthash.Hash, []byte)) (contenthash.Hash, error) {
	put := func(n Node) contenthash.Hash {
		b := Encode(n)
		h := HashOf(b)
		emit(h, b)
		return h
	}
	sizes := make([]int, len(entries))
	for i, e := range entries {
		sizes[i] = len(appendEntry(nil, e))
	}
	if len(entries) <= lim.maxItems && nodeSize(overheadDir, sizes) <= lim.maxBytes {
		return put(Node{Kind: KindDir, Entries: entries}), nil
	}
	groups, err := greedy(sizes, overheadDirPage, lim)
	if err != nil {
		return contenthash.Hash{}, err
	}
	refs := make([]PageRef, 0, len(groups))
	for _, g := range groups {
		h := put(Node{Kind: KindDirPage, Entries: entries[g.start:g.end]})
		refs = append(refs, PageRef{Count: int64(g.end - g.start), First: entries[g.start].Name, Hash: h})
	}
	for {
		rsizes := make([]int, len(refs))
		for i, r := range refs {
			rsizes[i] = len(appendPageRef(nil, r))
		}
		if len(refs) <= lim.maxItems && nodeSize(overheadDirIndex, rsizes) <= lim.maxBytes {
			return put(Node{Kind: KindDirIndex, Pages: refs}), nil
		}
		groups, err := greedy(rsizes, overheadDirIndex, lim)
		if err != nil {
			return contenthash.Hash{}, err
		}
		if len(groups) >= len(refs) {
			return contenthash.Hash{}, fmt.Errorf("%w: index level does not shrink", ErrInvalidEntry)
		}
		next := make([]PageRef, 0, len(groups))
		for _, g := range groups {
			var count int64
			for _, r := range refs[g.start:g.end] {
				count += r.Count
			}
			h := put(Node{Kind: KindDirIndex, Pages: refs[g.start:g.end]})
			next = append(next, PageRef{Count: count, First: refs[g.start].First, Hash: h})
		}
		refs = next
	}
}
