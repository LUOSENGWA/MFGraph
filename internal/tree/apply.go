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

// Change is one change of a commit (specification, section 2.6).
type Change struct {
	// Path is the path as the client gave it. It is looked up in the base
	// tree with the lookup rules; a file found there is replaced or
	// deleted and keeps its stored name. Otherwise the path is created and
	// the write-time rules apply to the names it creates.
	Path   string
	Delete bool             // delete the file found at Path
	Hash   contenthash.Hash // otherwise: the content of the file
	Size   int64
	// Mode is ModeRegular or ModeExecutable; 0 means not given: a replaced
	// file keeps its mode and a new file gets ModeRegular.
	Mode uint32
}

// Stats counts files and the sum of their sizes (or the change of both).
type Stats struct {
	Files int64
	Bytes int64
}

// Result is the outcome of Apply and Graft.
type Result struct {
	Root contenthash.Hash
	// Nodes holds the nodes of the new tree that were not read from the
	// base tree, by hash (canonical, uncompressed bytes). Storing them and
	// the nodes the base tree already has gives the complete new tree.
	Nodes map[contenthash.Hash][]byte
	Delta Stats    // files and bytes: new tree minus base tree
	Paths []string // stored path of each change, in input order
}

// Apply applies changes to the tree base and returns the new tree. Only the
// directories on the paths of the changes are rewritten; every other
// subtree is reused by hash. The result is exactly the tree that Build
// would produce for the resulting set of files.
//
// Every path is first checked with pathx.NormalizeLookup and then looked
// up with the lookup rules (LookupPath): the stored path given byte for byte
// first, then its NFC form.
//
//   - A file found there is replaced (Delete false) or deleted (Delete
//     true). The write-time rules are not applied: the file keeps its stored
//     name. Without Mode, a replaced file keeps its mode.
//   - Otherwise a Delete fails with ErrPathNotFound, and any other change
//     creates the path: the directories that exist are found segment by
//     segment (as given, then in NFC) and keep their stored names; the
//     segments that do not exist are new names, which must satisfy the
//     write-time rules (pathx.NormalizeSegment of the segment as given), are
//     stored in NFC and are checked with the sibling rules
//     (pathx.CheckSiblings) against the names the directory has in the base
//     tree and the names the other changes create in it: case_conflict,
//     file_dir_conflict, duplicate. A name of the base tree that another
//     change deletes still counts, so turning a file into a directory, or
//     changing only the case of a name, takes two commits. The new path must
//     be at most pathx.MaxPathBytes long.
//
// Two changes may not name the same path (the same NFC form): duplicate.
// Entries that the changes do not touch keep their stored bytes and are
// not checked again. Directories left without entries disappear (the root
// stays, possibly empty).
//
// Errors: *pathx.Error for paths that break a rule, ErrPathNotFound
// (wrapped) for a delete of a path that is not a file, ErrInvalidEntry for
// an invalid hash, size or mode, and errors of reading the base tree.
func Apply(ctx context.Context, g Getter, base contenthash.Hash, changes []Change) (Result, error) {
	return apply(ctx, g, base, changes, frozenLimits)
}

func apply(ctx context.Context, g Getter, base contenthash.Hash, changes []Change, lim limits) (Result, error) {
	a := newApplier(ctx, g, base, lim)
	paths := make([]string, len(changes))
	seen := make(map[string]int, len(changes))
	for i, c := range changes {
		p, err := a.add(c, i, seen)
		if err != nil {
			return Result{}, err
		}
		paths[i] = p
	}
	res, err := a.finish()
	res.Paths = paths
	return res, err
}

// Graft returns the tree base with the entry at the stored path segs
// replaced by e, a file or a directory of a tree in the same store (its
// subtree is reused by hash), or removed when e is nil. It is the operation
// of restoring one path from another snapshot: the names are stored bytes
// and no write-time rule is applied to them. Missing directories above segs
// are created with the names in segs; a file in their place gives a
// file_dir_conflict. Removing a path that does not exist gives
// ErrPathNotFound. Delta counts the files of the replaced and the inserted
// subtree (reading both).
func Graft(ctx context.Context, g Getter, base contenthash.Hash, segs []string, e *Entry) (Result, error) {
	return graft(ctx, g, base, segs, e, frozenLimits)
}

func graft(ctx context.Context, g Getter, base contenthash.Hash, segs []string, e *Entry, lim limits) (Result, error) {
	if len(segs) == 0 || len(segs) > MaxDepth {
		return Result{}, fmt.Errorf("tree: Graft needs a path of 1 to %d segments", MaxDepth)
	}
	for _, s := range segs {
		if err := pathx.ValidateStoredName(s); err != nil {
			return Result{}, err
		}
	}
	if e != nil {
		if err := validateEntry(Entry{Name: segs[len(segs)-1], Type: e.Type, Hash: e.Hash, Size: e.Size, Mode: e.Mode}); err != nil {
			return Result{}, err
		}
	}
	a := newApplier(ctx, g, base, lim)
	path := strings.Join(segs, "/")
	md := a.root
	cur := base
	for i, s := range segs[:len(segs)-1] {
		var found Entry
		ok := false
		if !cur.IsZero() {
			var err error
			if found, ok, err = a.r.find(cur, s); err != nil {
				return Result{}, err
			}
		}
		if ok && found.Type == TypeFile {
			return Result{}, &pathx.Error{Kind: pathx.KindFileDirConflict, Input: path,
				Detail: fmt.Sprintf("%q is a file", strings.Join(segs[:i+1], "/"))}
		}
		if !ok {
			cur = contenthash.Hash{}
		} else {
			cur = found.Hash
		}
		md = md.child(s, cur, ok)
	}
	leaf := segs[len(segs)-1]
	var old *Entry
	if !cur.IsZero() {
		o, ok, err := a.r.find(cur, leaf)
		if err != nil {
			return Result{}, err
		}
		if ok {
			old = &o
		}
	}
	if e == nil && old == nil {
		return Result{}, fmt.Errorf("%w: %q", ErrPathNotFound, path)
	}
	m := &mod{existed: old != nil, input: path}
	for _, x := range []struct {
		e    *Entry
		sign int64
	}{{old, -1}, {e, 1}} {
		if x.e == nil {
			continue
		}
		files, bytes := int64(1), x.e.Size
		if x.e.Type == TypeDir {
			var err error
			if files, bytes, err = count(a.r, x.e.Hash, len(segs)); err != nil {
				return Result{}, err
			}
		}
		a.delta.Files += x.sign * files
		a.delta.Bytes += x.sign * bytes
	}
	if e == nil {
		m.del = true
	} else {
		ne := *e
		ne.Name = leaf
		m.set = &ne
	}
	md.children[leaf] = m
	res, err := a.finish()
	res.Paths = []string{path}
	return res, err
}

type applier struct {
	r     *reader
	lim   limits
	base  contenthash.Hash
	root  *modDir
	nodes map[contenthash.Hash][]byte
	delta Stats
}

// modDir collects the changes of one directory.
type modDir struct {
	base     contenthash.Hash // top node in the base tree; zero for a new directory
	path     []string         // stored path
	children map[string]*mod
}

// mod is what happens to one name of a directory: set (a file, or for
// Graft any entry), del, or changes below it (sub).
type mod struct {
	set     *Entry
	del     bool
	sub     *modDir
	existed bool   // the name exists in the base directory
	created bool   // a new name of a commit: checked against its siblings
	input   string // the change's path, for errors
}

func newApplier(ctx context.Context, g Getter, base contenthash.Hash, lim limits) *applier {
	if base.IsZero() {
		base = EmptyRoot
	}
	return &applier{r: newReader(ctx, g), lim: lim, base: base, root: &modDir{base: base, children: map[string]*mod{}},
		nodes: map[contenthash.Hash][]byte{}}
}

// child returns the modDir of the subdirectory name (creating it), whose
// base top node is h (zero for a new directory).
func (md *modDir) child(name string, h contenthash.Hash, existed bool) *modDir {
	if m := md.children[name]; m != nil && m.sub != nil {
		return m.sub
	}
	sub := &modDir{base: h, path: append(md.path[:len(md.path):len(md.path)], name), children: map[string]*mod{}}
	md.children[name] = &mod{sub: sub, existed: existed}
	return sub
}

func pathErr(kind pathx.Kind, input, detail string) error {
	return &pathx.Error{Kind: kind, Input: input, Detail: detail}
}

// add records change i and returns its stored path.
func (a *applier) add(c Change, i int, seen map[string]int) (string, error) {
	nfc, raw, err := pathx.NormalizeLookup(c.Path)
	if err != nil {
		return "", err
	}
	if prev, dup := seen[nfc]; dup {
		return "", pathErr(pathx.KindDuplicate, c.Path, fmt.Sprintf("the same path as change %d", prev+1))
	}
	seen[nfc] = i
	if !c.Delete {
		if c.Hash.IsZero() {
			return "", fmt.Errorf("%w: change %d has no content hash", ErrInvalidEntry, i+1)
		}
		if c.Size < 0 || c.Size > MaxSafeInteger {
			return "", fmt.Errorf("%w: change %d: size %d out of range", ErrInvalidEntry, i+1, c.Size)
		}
		if c.Mode != 0 && c.Mode != ModeRegular && c.Mode != ModeExecutable {
			return "", fmt.Errorf("%w: change %d: mode %d is not 420 or 493", ErrInvalidEntry, i+1, c.Mode)
		}
	}
	rawSegs, nfcSegs := split(raw), split(nfc)
	lookups := [][]string{rawSegs, nfcSegs}
	if raw == nfc {
		lookups[1] = nil
	}

	// Replace or delete: a file found with the lookup rules.
	for _, segs := range lookups {
		if len(segs) == 0 {
			continue // the NFC form equals the input
		}
		e, dirs, err := lookup(a.r, a.base, segs)
		if err != nil {
			if errors.Is(err, ErrPathNotFound) {
				continue
			}
			return "", err
		}
		if e.Type != TypeFile {
			break // a directory: creating a file there conflicts (below)
		}
		md := a.root
		for j, s := range segs[:len(segs)-1] {
			if m := md.children[s]; m != nil && m.sub == nil {
				return "", pathErr(pathx.KindFileDirConflict, c.Path, fmt.Sprintf("%q is changed as a file", strings.Join(segs[:j+1], "/")))
			}
			md = md.child(s, dirs[j+1], true)
		}
		name := segs[len(segs)-1]
		if m := md.children[name]; m != nil {
			if m.sub != nil {
				return "", pathErr(pathx.KindFileDirConflict, c.Path, "used as a directory by another change")
			}
			return "", pathErr(pathx.KindDuplicate, c.Path, "the same file as another change")
		}
		old := e
		m := &mod{existed: true, input: c.Path}
		if c.Delete {
			m.del = true
			a.delta.Files--
			a.delta.Bytes -= old.Size
		} else {
			mode := c.Mode
			if mode == 0 {
				mode = old.Mode
			}
			m.set = &Entry{Name: name, Type: TypeFile, Hash: c.Hash, Size: c.Size, Mode: mode}
			a.delta.Bytes += c.Size - old.Size
		}
		md.children[name] = m
		return strings.Join(segs, "/"), nil
	}
	if c.Delete {
		return "", fmt.Errorf("%w: %q is not a file of the base snapshot", ErrPathNotFound, c.Path)
	}

	// Create: find the existing directories segment by segment.
	n := len(rawSegs)
	stored := make([]string, 0, n)
	md, cur, k := a.root, a.base, 0
	for ; k < n-1; k++ {
		var found Entry
		ok := false
		if !cur.IsZero() {
			for _, s := range uniq(rawSegs[k], nfcSegs[k]) {
				if found, ok, err = a.r.find(cur, s); err != nil {
					return "", err
				}
				if ok {
					break
				}
			}
		}
		if !ok {
			break
		}
		if found.Type == TypeFile {
			return "", pathErr(pathx.KindFileDirConflict, c.Path, fmt.Sprintf("%q is a file", joinSegs(stored, found.Name)))
		}
		if m := md.children[found.Name]; m != nil && m.sub == nil {
			return "", pathErr(pathx.KindFileDirConflict, c.Path, fmt.Sprintf("%q is changed as a file", joinSegs(stored, found.Name)))
		}
		stored = append(stored, found.Name)
		md = md.child(found.Name, found.Hash, true)
		cur = found.Hash
	}
	// The new names, checked with the write-time rules as given.
	names := make([]string, 0, n-k)
	for _, s := range rawSegs[k:] {
		name, err := pathx.NormalizeSegment(s)
		if err != nil {
			kind := pathx.KindOf(err)
			return "", pathErr(kind, c.Path, err.Error())
		}
		names = append(names, name)
	}
	full := strings.Join(append(append([]string{}, stored...), names...), "/")
	if len(full) > pathx.MaxPathBytes {
		return "", pathErr(pathx.KindPathTooLong, c.Path, fmt.Sprintf("the new path has %d bytes, limit %d", len(full), pathx.MaxPathBytes))
	}
	for j, name := range names {
		last := j == len(names)-1
		m := md.children[name]
		if m == nil && !cur.IsZero() {
			// A name of the base directory (stored in another form than the
			// one this change was looked up with).
			e, ok, err := a.r.find(cur, name)
			if err != nil {
				return "", err
			}
			if ok {
				if e.Type == TypeFile && !last || e.Type == TypeDir && last {
					return "", pathErr(pathx.KindFileDirConflict, c.Path, fmt.Sprintf("%q exists as a %s", joinSegs(stored, name), e.Type))
				}
				return "", pathErr(pathx.KindDuplicate, c.Path, fmt.Sprintf("%q exists", joinSegs(stored, name)))
			}
		}
		if last {
			if m != nil {
				if m.sub != nil {
					return "", pathErr(pathx.KindFileDirConflict, c.Path, "used as a directory by another change")
				}
				return "", pathErr(pathx.KindDuplicate, c.Path, "the same file as another change")
			}
			mode := c.Mode
			if mode == 0 {
				mode = ModeRegular
			}
			md.children[name] = &mod{set: &Entry{Name: name, Type: TypeFile, Hash: c.Hash, Size: c.Size, Mode: mode},
				created: true, input: c.Path}
			a.delta.Files++
			a.delta.Bytes += c.Size
			break
		}
		if m != nil && m.sub == nil {
			return "", pathErr(pathx.KindFileDirConflict, c.Path, fmt.Sprintf("%q is a file of another change", joinSegs(stored, name)))
		}
		if m == nil {
			sub := &modDir{path: append(append([]string{}, stored...), name), children: map[string]*mod{}}
			md.children[name] = &mod{sub: sub, created: true, input: c.Path}
			m = md.children[name]
		}
		md, cur = m.sub, m.sub.base
		stored = append(stored, name)
	}
	return full, nil
}

func uniq(a, b string) []string {
	if a == b {
		return []string{a}
	}
	return []string{a, b}
}

// finish rewrites the changed directories bottom-up.
func (a *applier) finish() (Result, error) {
	root, _, err := a.applyDir(a.root, 0)
	if err != nil {
		return Result{}, err
	}
	// Nodes read from the base tree are not new.
	for h := range a.nodes {
		if _, read := a.r.cache[h]; read || h == a.base {
			delete(a.nodes, h)
		}
	}
	return Result{Root: root, Nodes: a.nodes, Delta: a.delta}, nil
}

func (a *applier) put(n Node) contenthash.Hash {
	b := Encode(n)
	h := HashOf(b)
	a.nodes[h] = b
	return h
}

// update is the new state of one name: entry nil removes it.
type update struct {
	name    string
	entry   *Entry
	existed bool
	created bool
	input   string
}

// applyDir rewrites one directory after its subdirectories and returns its
// new hash; empty reports a directory without entries.
func (a *applier) applyDir(md *modDir, depth int) (contenthash.Hash, bool, error) {
	if depth > MaxDepth {
		return contenthash.Hash{}, false, fmt.Errorf("%w: more than %d levels", ErrTooDeep, MaxDepth)
	}
	names := make([]string, 0, len(md.children))
	for name := range md.children {
		names = append(names, name)
	}
	sort.Strings(names)
	ups := make([]update, 0, len(names))
	for _, name := range names {
		m := md.children[name]
		u := update{name: name, existed: m.existed, created: m.created, input: m.input}
		switch {
		case m.sub != nil:
			h, empty, err := a.applyDir(m.sub, depth+1)
			if err != nil {
				return contenthash.Hash{}, false, err
			}
			if !empty {
				u.entry = &Entry{Name: name, Type: TypeDir, Hash: h}
			}
		case m.set != nil:
			u.entry = m.set
		}
		if u.entry == nil && !u.existed {
			continue // nothing to remove
		}
		ups = append(ups, u)
	}
	return a.rewrite(md, ups, depth)
}

func (a *applier) rewrite(md *modDir, ups []update, depth int) (contenthash.Hash, bool, error) {
	if len(ups) == 0 && !md.base.IsZero() {
		return md.base, false, nil // nothing changes here
	}
	var top Node
	if !md.base.IsZero() {
		var err error
		if top, err = a.r.top(md.base); err != nil {
			return contenthash.Hash{}, false, err
		}
	}
	created := false
	for _, u := range ups {
		created = created || u.created
	}
	if top.Kind == KindDirIndex && !created {
		var total int64
		for _, p := range top.Pages {
			total += p.Count
		}
		for _, u := range ups {
			switch {
			case u.entry != nil && !u.existed:
				total++
			case u.entry == nil && u.existed:
				total--
			}
		}
		if total > int64(a.lim.maxItems) {
			h, err := a.rewritePaged(md, top, ups)
			return h, false, err
		}
	}
	// Whole directory: read every entry and lay it out again.
	var entries []Entry
	if !md.base.IsZero() {
		it, err := openDir(a.r, md.base, "")
		if err != nil {
			return contenthash.Hash{}, false, err
		}
		for {
			e, ok, err := it.peek()
			if err != nil {
				return contenthash.Hash{}, false, err
			}
			if !ok {
				break
			}
			entries = append(entries, e)
			if err := it.next(); err != nil {
				return contenthash.Hash{}, false, err
			}
		}
	}
	if created {
		if err := checkCreated(entries, ups); err != nil {
			return contenthash.Hash{}, false, err
		}
	}
	entries = merge(entries, ups)
	if len(entries) == 0 && depth > 0 {
		return contenthash.Hash{}, true, nil
	}
	h, err := layout(entries, a.lim, func(h contenthash.Hash, b []byte) { a.nodes[h] = b })
	return h, false, err
}

// merge applies the sorted updates to the sorted entries.
func merge(entries []Entry, ups []update) []Entry {
	out := make([]Entry, 0, len(entries)+len(ups))
	i := 0
	for _, u := range ups {
		for i < len(entries) && entries[i].Name < u.name {
			out = append(out, entries[i])
			i++
		}
		if i < len(entries) && entries[i].Name == u.name {
			i++ // replaced or removed
		}
		if u.entry != nil {
			out = append(out, *u.entry)
		}
	}
	return append(out, entries[i:]...)
}

// foldKey is pathx.FoldKey with a fast path: for ASCII the canonical
// caseless form is the ASCII lowercase form.
func foldKey(name string) string {
	for i := 0; i < len(name); i++ {
		if name[i] >= 0x80 {
			return pathx.FoldKey(name)
		}
	}
	return strings.ToLower(name)
}

// checkCreated applies the case rule of the sibling rules to the names a
// commit creates in one directory: none may have the fold key of another
// name created there or of a name of the base directory (base), whether or
// not another change deletes it. (A created name equal to another name,
// or used as both a file and a directory, is rejected when the changes are
// recorded.)
func checkCreated(base []Entry, ups []update) error {
	keys := map[string]update{}
	for _, u := range ups {
		if !u.created || u.entry == nil {
			continue
		}
		k := foldKey(u.name)
		if prev, dup := keys[k]; dup {
			return pathErr(pathx.KindCaseConflict, u.input, fmt.Sprintf("%q conflicts with %q", u.name, prev.name))
		}
		keys[k] = u
	}
	for _, e := range base {
		if u, ok := keys[foldKey(e.Name)]; ok && u.name != e.Name {
			return pathErr(pathx.KindCaseConflict, u.input, fmt.Sprintf("%q conflicts with %q", u.name, e.Name))
		}
	}
	return nil
}

// leafRefs returns the page references of a paged directory in order. The
// references of one index level all point to nodes of the same kind
// (pages, or indexes of the next level): the kind is learnt from the node
// that may contain hint, which is read anyway, and only index nodes are
// read to go down a level.
func (a *applier) leafRefs(top Node, hint string) ([]PageRef, error) {
	level := top.Pages
	for depth := 0; ; depth++ {
		if depth >= maxIndexDepth {
			return nil, fmt.Errorf("%w: more than %d index levels", ErrTooDeep, maxIndexDepth)
		}
		probe, err := a.r.child(level[refFor(level, hint)].Hash)
		if err != nil {
			return nil, err
		}
		if probe.Kind == KindDirPage {
			return level, nil
		}
		var next []PageRef
		for _, ref := range level {
			n, err := a.r.child(ref.Hash)
			if err != nil {
				return nil, err
			}
			if n.Kind != KindDirIndex {
				return nil, fmt.Errorf("%w: an index level mixes pages and indexes", ErrInvalidTree)
			}
			next = append(next, n.Pages...)
		}
		level = next
	}
}

// rewritePaged rewrites a paged directory that stays paged and gains no new
// names: the pages before the first changed name are reused without
// reading them; from that page on, the entries are packed again with the
// greedy rule of the specification until all changes are applied and a new
// page starts where an old one started, from where the old pages are
// reused again. The result equals layout over all entries.
func (a *applier) rewritePaged(md *modDir, top Node, ups []update) (contenthash.Hash, error) {
	refs, err := a.leafRefs(top, ups[0].name)
	if err != nil {
		return contenthash.Hash{}, err
	}
	start := refFor(refs, ups[0].name)
	p := &pager{lim: a.lim, put: a.put}
	p.refs = append(p.refs, refs[:start]...)
	u := 0
	j := start
	for ; j < len(refs); j++ {
		// Once every update is applied, the old pages are reused from the
		// first one that starts a new page: its entries and all that follow
		// are unchanged, so greedy packs them as before. A next entry starts
		// a new page when the current page is empty or full by count
		// (decided without reading it), or when it does not fit by size.
		if u == len(ups) && (len(p.cur) == 0 || len(p.cur) == a.lim.maxItems) {
			break
		}
		n, err := a.r.child(refs[j].Hash)
		if err != nil {
			return contenthash.Hash{}, err
		}
		if n.Kind != KindDirPage || int64(len(n.Entries)) != refs[j].Count || n.Entries[0].Name != refs[j].First {
			return contenthash.Hash{}, fmt.Errorf("%w: directory %q: reference %d does not match its page", ErrInvalidTree,
				strings.Join(md.path, "/"), j)
		}
		if u == len(ups) && p.wouldClose(n.Entries[0]) {
			break
		}
		// Updates that sort before the next page belong to this one.
		end := len(ups)
		if j+1 < len(refs) {
			end = u + sort.Search(len(ups)-u, func(i int) bool { return ups[u+i].name >= refs[j+1].First })
		}
		for _, e := range merge(n.Entries, ups[u:end]) {
			if err := p.add(e); err != nil {
				return contenthash.Hash{}, err
			}
		}
		u = end
	}
	p.close()
	if j < len(refs) {
		p.refs = append(p.refs, refs[j:]...)
	}
	return layoutIndex(p.refs, a.lim, a.put)
}

// pager packs entries into dir_page nodes with the greedy rule (the same
// as greedy over the entry sizes): a page is closed when one more entry
// would exceed the entry or the byte limit.
type pager struct {
	lim  limits
	put  func(Node) contenthash.Hash
	cur  []Entry
	size int // encoded size of cur as a dir_page
	refs []PageRef
}

func (p *pager) added(e Entry) (int, int) {
	s := len(appendEntry(nil, e))
	if len(p.cur) == 0 {
		return overheadDirPage + s, s
	}
	return p.size + 1 + s, s
}

// wouldClose reports whether adding e closes the current page first.
func (p *pager) wouldClose(e Entry) bool {
	size, _ := p.added(e)
	return len(p.cur) > 0 && (len(p.cur)+1 > p.lim.maxItems || size > p.lim.maxBytes)
}

func (p *pager) add(e Entry) error {
	if p.wouldClose(e) {
		p.close()
	}
	size, s := p.added(e)
	if size > p.lim.maxBytes {
		return fmt.Errorf("%w: a single item of %d bytes does not fit into a node", ErrInvalidEntry, s)
	}
	p.cur = append(p.cur, e)
	p.size = size
	return nil
}

func (p *pager) close() {
	if len(p.cur) == 0 {
		return
	}
	h := p.put(Node{Kind: KindDirPage, Entries: p.cur})
	p.refs = append(p.refs, PageRef{Count: int64(len(p.cur)), First: p.cur[0].Name, Hash: h})
	p.cur, p.size = nil, 0
}
