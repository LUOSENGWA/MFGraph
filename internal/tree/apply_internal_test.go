package tree

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/text/unicode/norm"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/pathx"
)

var bg = context.Background()

// testLimits are the limit sets the property tests run with: the frozen
// ones and reduced ones that page small directories (by count, and by
// size) and add index levels.
var testLimits = []limits{
	frozenLimits,
	{maxItems: 4, maxBytes: MaxNodeBytes},
	{maxItems: 3, maxBytes: MaxNodeBytes},
	{maxItems: 6, maxBytes: 700},
}

// countingGetter counts the nodes read.
type countingGetter struct {
	m MapGetter
	n atomic.Int64
}

func (c *countingGetter) GetNode(ctx context.Context, h contenthash.Hash) ([]byte, error) {
	c.n.Add(1)
	return c.m.GetNode(ctx, h)
}

// model is a set of files by path (NFC).
type model map[string]FileEntry

func (m model) list() []FileEntry {
	out := make([]FileEntry, 0, len(m))
	for _, f := range m {
		out = append(out, f)
	}
	return out
}

func (m model) clone() model {
	out := model{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func content(r *rand.Rand, p string) (contenthash.Hash, int64) {
	c := fmt.Sprintf("%s#%d", p, r.Intn(1<<20))
	if r.Intn(10) == 0 {
		c = "" // empty files are valid
	}
	return contenthash.SumBytes([]byte(c)), int64(len(c))
}

// The alphabet has case variants (case conflicts), "U+00E9" and "e"+U+0301
// (NFC), and names used both as a file and as a directory.
var alphabet = []string{"a", "A", "b", "c", "x", "\u00e9", "\u6587", "\U0001F680", "q\"", "s p"}

func randomPath(r *rand.Rand) string {
	depth := 1 + r.Intn(3)
	segs := make([]string, depth)
	for i := range segs {
		segs[i] = alphabet[r.Intn(len(alphabet))]
		if r.Intn(3) == 0 {
			segs[i] += fmt.Sprint(r.Intn(3))
		}
	}
	return strings.Join(segs, "/")
}

// randomBase builds a valid random tree (paths that conflict are skipped).
func randomBase(t *testing.T, r *rand.Rand, n int, lim limits) (model, contenthash.Hash, map[contenthash.Hash][]byte) {
	t.Helper()
	m := model{}
	for tries := 0; len(m) < n && tries < 20*n; tries++ {
		p := randomPath(r)
		h, size := content(r, p)
		mode := ModeRegular
		if r.Intn(4) == 0 {
			mode = ModeExecutable
		}
		next := m.clone()
		next[p] = FileEntry{Path: p, Hash: h, Size: size, Mode: mode}
		if _, _, err := build(next.list(), lim); err != nil {
			continue
		}
		m = next
	}
	root, nodes, err := build(m.list(), lim)
	if err != nil {
		t.Fatal(err)
	}
	return m, root, nodes
}

// nfd returns p with "U+00E9" decomposed, as some clients send it.
func nfd(p string) string { return strings.ReplaceAll(p, "\u00e9", "e\u0301") }

// TestApplyMatchesBuild is the property that makes the incremental tree
// update trustworthy: for random trees and random batches of changes, Apply
// gives exactly the root that Build gives for the resulting set of files,
// fails exactly when a rule is broken, and returns every node of the new
// tree that the base does not have.
func TestApplyMatchesBuild(t *testing.T) {
	for li, lim := range testLimits {
		r := rand.New(rand.NewSource(int64(100 + li)))
		trials := 400
		if testing.Short() {
			trials = 60
		}
		for trial := 0; trial < trials; trial++ {
			base, baseRoot, baseNodes := randomBase(t, r, r.Intn(40), lim)
			paths := make([]string, 0, len(base))
			for p := range base {
				paths = append(paths, p)
			}
			sort.Strings(paths)

			var changes []Change
			final := base.clone()
			union := base.clone() // base plus every created path: what the sibling rules see
			dup, badDelete := false, false
			seen := map[string]bool{}
			for k := 1 + r.Intn(6); k > 0; k-- {
				var c Change
				switch op := r.Intn(10); {
				case op < 3 && len(paths) > 0: // replace
					p := paths[r.Intn(len(paths))]
					c.Path = p
					c.Hash, c.Size = content(r, p)
					c.Mode = []uint32{0, ModeRegular, ModeExecutable}[r.Intn(3)]
				case op < 5 && len(paths) > 0: // delete
					c.Path, c.Delete = paths[r.Intn(len(paths))], true
				case op == 5: // delete something that may not be a file
					c.Path, c.Delete = randomPath(r), true
				default: // create (or replace, if it exists)
					c.Path = randomPath(r)
					c.Hash, c.Size = content(r, c.Path)
					c.Mode = []uint32{0, ModeRegular, ModeExecutable}[r.Intn(3)]
				}
				key := norm.NFC.String(c.Path)
				if r.Intn(4) == 0 {
					c.Path = nfd(c.Path) // the same path in another normalization
				}
				if seen[key] {
					dup = true
				}
				seen[key] = true
				changes = append(changes, c)
				old, isFile := base[key]
				switch {
				case c.Delete && !isFile:
					badDelete = true
				case c.Delete:
					delete(final, key)
				default:
					mode := c.Mode
					if mode == 0 {
						mode = ModeRegular
						if isFile {
							mode = old.Mode
						}
					}
					f := FileEntry{Path: key, Hash: c.Hash, Size: c.Size, Mode: mode}
					final[key] = f
					union[key] = f
				}
			}
			_, _, unionErr := build(union.list(), lim)
			res, err := apply(bg, MapGetter(baseNodes), baseRoot, changes, lim)
			what := fmt.Sprintf("limits %+v trial %d changes %+v", lim, trial, changes)
			if err != nil {
				kind := pathx.KindOf(err)
				ok := dup && kind == pathx.KindDuplicate || badDelete && errors.Is(err, ErrPathNotFound) ||
					unionErr != nil && (kind == pathx.KindCaseConflict || kind == pathx.KindFileDirConflict || kind == pathx.KindDuplicate)
				if !ok {
					t.Fatalf("%s: Apply failed: %v (dup %v, bad delete %v, union %v)", what, err, dup, badDelete, unionErr)
				}
				continue
			}
			if dup || badDelete || unionErr != nil {
				t.Fatalf("%s: Apply succeeded, want an error (dup %v, bad delete %v, union %v)", what, dup, badDelete, unionErr)
			}
			wantRoot, wantNodes, err := build(final.list(), lim)
			if err != nil {
				t.Fatalf("%s: Build of the result: %v", what, err)
			}
			if res.Root != wantRoot {
				t.Fatalf("%s: Apply root %s, Build root %s", what, res.Root, wantRoot)
			}
			// Every node of the new tree is in the base or in res.Nodes, and
			// res.Nodes holds nothing else.
			for h := range wantNodes {
				if _, inBase := baseNodes[h]; !inBase {
					if _, ok := res.Nodes[h]; !ok {
						t.Fatalf("%s: node %s of the new tree is missing from the result", what, h)
					}
				}
			}
			for h := range res.Nodes {
				if _, ok := wantNodes[h]; !ok && h != EmptyRoot {
					t.Fatalf("%s: the result has node %s that is not in the new tree", what, h)
				}
			}
			// The delta matches the file sets.
			var dFiles, dBytes int64
			for _, f := range final {
				dFiles++
				dBytes += f.Size
			}
			for _, f := range base {
				dFiles--
				dBytes -= f.Size
			}
			if res.Delta != (Stats{Files: dFiles, Bytes: dBytes}) {
				t.Fatalf("%s: delta %+v, want %d files %d bytes", what, res.Delta, dFiles, dBytes)
			}
		}
	}
}

func fe(p string, content string, mode uint32) FileEntry {
	return FileEntry{Path: p, Hash: contenthash.SumBytes([]byte(content)), Size: int64(len(content)), Mode: mode}
}

func put(p, content string) Change {
	return Change{Path: p, Hash: contenthash.SumBytes([]byte(content)), Size: int64(len(content))}
}

func del(p string) Change { return Change{Path: p, Delete: true} }

// TestApplyWritesOnlyThePath: replacing one file writes exactly the
// directories from that file up to the root; in a paged directory the page
// of the file and the index nodes above it; and reads about as many nodes.
func TestApplyWritesOnlyThePath(t *testing.T) {
	files := []FileEntry{fe("a/b/c/f.txt", "1", ModeRegular), fe("a/b/g.txt", "2", ModeRegular), fe("a/h.txt", "3", ModeRegular),
		fe("z/y.txt", "4", ModeRegular), fe("top.txt", "5", ModeRegular)}
	root, nodes, err := Build(files)
	if err != nil {
		t.Fatal(err)
	}
	g := &countingGetter{m: nodes}
	res, err := Apply(bg, g, root, []Change{put("a/b/c/f.txt", "changed")})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nodes) != 4 || g.n.Load() != 4 {
		t.Fatalf("one change four levels deep: %d new nodes, %d reads; want 4 and 4", len(res.Nodes), g.n.Load())
	}

	// A flat directory of 5000 files: 5 pages (1024 entries) and one index.
	var big []FileEntry
	for i := 0; i < 5000; i++ {
		big = append(big, fe(fmt.Sprintf("d/f%05d", i), fmt.Sprint(i), ModeRegular))
	}
	root, nodes, err = Build(big)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		change     Change
		nodes      int
		readsAtMax int64
	}{
		// root, the index of d, and the page: 3 new nodes; reads: root,
		// index, the page of f02000 (the next page is not needed).
		{put("d/f02000", "new"), 3, 3},
		// A delete shifts every later page: f02000 is on the second page
		// (1024-2047), so the pages 2..5 are rewritten, plus the index and
		// the root; reads: root, index, pages 2..5.
		{del("d/f02000"), 4 + 2, 6},
	} {
		g := &countingGetter{m: nodes}
		res, err := Apply(bg, g, root, []Change{tc.change})
		if err != nil {
			t.Fatal(err)
		}
		want, _, _ := Build(applyModel(big, tc.change))
		if res.Root != want || len(res.Nodes) != tc.nodes || g.n.Load() > tc.readsAtMax {
			t.Errorf("%+v: root ok %v, %d new nodes (want %d), %d reads (at most %d)", tc.change, res.Root == want,
				len(res.Nodes), tc.nodes, g.n.Load(), tc.readsAtMax)
		}
	}
}

// applyModel applies replace and delete changes to a file list.
func applyModel(files []FileEntry, cs ...Change) []FileEntry {
	m := map[string]FileEntry{}
	for _, f := range files {
		m[f.Path] = f
	}
	for _, c := range cs {
		if c.Delete {
			delete(m, c.Path)
			continue
		}
		mode := c.Mode
		if mode == 0 {
			mode = ModeRegular
			if old, ok := m[c.Path]; ok {
				mode = old.Mode
			}
		}
		m[c.Path] = FileEntry{Path: c.Path, Hash: c.Hash, Size: c.Size, Mode: mode}
	}
	out := make([]FileEntry, 0, len(m))
	for _, f := range m {
		out = append(out, f)
	}
	return out
}

// storedTree encodes a tree by hand, so that it can hold names the
// write-time rules reject today (as another implementation may write them).
func storedTree(entries ...Entry) (contenthash.Hash, MapGetter) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	b := Encode(Node{Kind: KindDir, Entries: entries})
	h := HashOf(b)
	return h, MapGetter{h: b}
}

func fileEntry(name, content string, mode uint32) Entry {
	return Entry{Name: name, Type: TypeFile, Hash: contenthash.SumBytes([]byte(content)), Size: int64(len(content)), Mode: mode}
}

func TestApplyRules(t *testing.T) {
	root, nodes, err := Build([]FileEntry{fe("docs/a.txt", "a", ModeRegular), fe("tool.sh", "t", ModeExecutable),
		fe("\u00e9.txt", "e", ModeRegular), fe("Readme", "r", ModeRegular)})
	if err != nil {
		t.Fatal(err)
	}
	g := MapGetter(nodes)
	errCase := func(name string, cs []Change, kind pathx.Kind, notFound bool) {
		t.Helper()
		_, err := Apply(bg, g, root, cs)
		if notFound && !errors.Is(err, ErrPathNotFound) || !notFound && pathx.KindOf(err) != kind {
			t.Errorf("%s: err = %v, want %s (not found %v)", name, err, kind, notFound)
		}
	}
	errCase("invalid path", []Change{put("../evil", "x")}, pathx.KindDotSegment, false)
	errCase("absolute", []Change{put("/abs", "x")}, pathx.KindAbsolute, false)
	errCase("empty segment", []Change{put("a//b", "x")}, pathx.KindEmptySegment, false)
	errCase("backslash", []Change{put(`back\slash`, "x")}, pathx.KindBackslash, false)
	errCase("new name with a disallowed character", []Change{put("docs/b\u200b.txt", "x")}, pathx.KindDisallowedChar, false)
	errCase("new name unassigned in V", []Change{put("x\U0001F6D8", "x")}, pathx.KindUnassignedChar, false)
	errCase("duplicate in NFC", []Change{put("\u00e9.txt", "1"), put("e\u0301.txt", "2")}, pathx.KindDuplicate, false)
	errCase("case conflict with a base name", []Change{put("docs/A.txt", "x")}, pathx.KindCaseConflict, false)
	errCase("case conflict of a new directory", []Change{put("Docs/b.txt", "x")}, pathx.KindCaseConflict, false)
	errCase("case conflict between two new names", []Change{put("n.txt", "1"), put("N.txt", "2")}, pathx.KindCaseConflict, false)
	errCase("case conflict with a deleted name", []Change{del("Readme"), put("README", "x")}, pathx.KindCaseConflict, false)
	errCase("file below a file", []Change{put("tool.sh/x", "x")}, pathx.KindFileDirConflict, false)
	errCase("file in place of a directory", []Change{put("docs", "x")}, pathx.KindFileDirConflict, false)
	errCase("file and directory in one commit", []Change{put("n", "1"), put("n/x", "2")}, pathx.KindFileDirConflict, false)
	errCase("directory and file in one commit", []Change{put("n/x", "1"), put("n", "2")}, pathx.KindFileDirConflict, false)
	errCase("delete a directory", []Change{del("docs")}, "", true)
	errCase("delete a missing file", []Change{del("nope.txt")}, "", true)
	errCase("path too long", []Change{put(strings.Repeat("d/", 2048)+"x", "x")}, pathx.KindPathTooLong, false)
	if _, err := Apply(bg, g, root, []Change{{Path: "x", Hash: contenthash.SumBytes(nil), Mode: 0o600}}); !errors.Is(err, ErrInvalidEntry) {
		t.Errorf("mode 0600: %v", err)
	}

	// Modes: an explicit mode wins, a replaced file keeps its mode, a new file gets 420.
	res, err := Apply(bg, g, root, []Change{put("tool.sh", "t2"), put("new.sh", "n"),
		{Path: "docs/a.txt", Hash: contenthash.SumBytes([]byte("a2")), Size: 2, Mode: ModeExecutable}})
	if err != nil {
		t.Fatal(err)
	}
	all := map[string]Entry{}
	merged := MapGetter{}
	for h, b := range nodes {
		merged[h] = b
	}
	for h, b := range res.Nodes {
		merged[h] = b
	}
	if err := Walk(bg, merged, res.Root, func(p string, e Entry) error { all[p] = e; return nil }); err != nil {
		t.Fatal(err)
	}
	if all["tool.sh"].Mode != ModeExecutable || all["new.sh"].Mode != ModeRegular || all["docs/a.txt"].Mode != ModeExecutable {
		t.Errorf("modes: tool.sh %d, new.sh %d, docs/a.txt %d", all["tool.sh"].Mode, all["new.sh"].Mode, all["docs/a.txt"].Mode)
	}
	// Replacing with NFD input finds the NFC name and keeps it.
	res, err = Apply(bg, g, root, []Change{put("e\u0301.txt", "e2")})
	if err != nil || res.Paths[0] != "\u00e9.txt" {
		t.Fatalf("replace by NFD input: %v %q", err, res.Paths)
	}
	// Deleting the last file of a directory removes the directory.
	res, err = Apply(bg, g, root, []Change{del("docs/a.txt")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lookup(bg, mergedWith(nodes, res.Nodes), res.Root, []string{"docs"}); !errors.Is(err, ErrPathNotFound) {
		t.Errorf("empty directory still present: %v", err)
	}
	// Deleting everything leaves the empty root.
	res, err = Apply(bg, g, root, []Change{del("docs/a.txt"), del("tool.sh"), del("\u00e9.txt"), del("Readme")})
	if err != nil || res.Root != EmptyRoot || res.Delta.Files != -4 {
		t.Fatalf("delete all: %v %s %+v", err, res.Root, res.Delta)
	}
	// An unchanged result is the base root.
	res, err = Apply(bg, g, root, []Change{{Path: "tool.sh", Hash: contenthash.SumBytes([]byte("t")), Size: 1}})
	if err != nil || res.Root != root || len(res.Nodes) != 0 {
		t.Fatalf("no-op change: %v %s %d", err, res.Root, len(res.Nodes))
	}
	// The empty tree (zero or EmptyRoot) is a valid base.
	for _, b := range []contenthash.Hash{{}, EmptyRoot} {
		res, err = Apply(bg, MapGetter{}, b, []Change{put("a/b.txt", "x")})
		if err != nil || res.Delta.Files != 1 {
			t.Fatalf("commit on the empty tree: %v %+v", err, res)
		}
	}
}

func mergedWith(a, b map[contenthash.Hash][]byte) MapGetter {
	m := MapGetter{}
	for h, x := range a {
		m[h] = x
	}
	for h, x := range b {
		m[h] = x
	}
	return m
}

// TestApplyStoredNames: names written by another implementation keep their
// bytes and are found by them; the write-time rules apply only to the names
// a change creates.
func TestApplyStoredNames(t *testing.T) {
	zw := fileEntry("a\u200bb", "zw", ModeRegular)          // disallowed today
	future := fileEntry("x\U0001F6D8", "17.0", ModeRegular) // assigned only after V
	decomposed := fileEntry("e\u0301.md", "nfd", ModeRegular)
	root, g := storedTree(zw, future, decomposed)
	for _, c := range []Change{put("a\u200bb", "new"), put("x\U0001F6D8", "new"), del("e\u0301.md"), put("new.txt", "n")} {
		if _, err := Apply(bg, g, root, []Change{c}); err != nil {
			t.Errorf("%q: %v", c.Path, err)
		}
	}
	// The NFC form of a stored NFD name does not find it, and creating it
	// conflicts with it (the fold key is computed on NFD).
	if _, err := Apply(bg, g, root, []Change{put("\u00e9.md", "x")}); pathx.KindOf(err) != pathx.KindCaseConflict {
		t.Errorf("NFC next to a stored NFD name: %v", err)
	}
	// Carried over entries keep their bytes.
	res, err := Apply(bg, g, root, []Change{put("new.txt", "n")})
	if err != nil {
		t.Fatal(err)
	}
	ls, _, err := ListDir(bg, mergedWith(g, res.Nodes), res.Root, "", 10)
	if err != nil || len(ls) != 4 || ls[0].Name != zw.Name || ls[1].Name != decomposed.Name {
		t.Fatalf("listing: %+v %v", ls, err)
	}
}

func TestGraft(t *testing.T) {
	root, nodes, err := Build([]FileEntry{fe("docs/a.txt", "a", ModeRegular), fe("docs/b.txt", "b", ModeRegular),
		fe("tool.sh", "t", ModeExecutable)})
	if err != nil {
		t.Fatal(err)
	}
	target, tnodes, err := Build([]FileEntry{fe("docs/a.txt", "old a", ModeRegular), fe("lib/x/y.txt", "y", ModeRegular),
		fe("lib/z.txt", "zz", ModeRegular)})
	if err != nil {
		t.Fatal(err)
	}
	g := mergedWith(nodes, tnodes)
	entryAt := func(r contenthash.Hash, p string) *Entry {
		e, err := Lookup(bg, g, r, split(p))
		if err != nil {
			t.Fatal(err)
		}
		return &e
	}
	check := func(name string, segs []string, e *Entry, want []FileEntry, delta Stats) {
		t.Helper()
		res, err := Graft(bg, g, root, segs, e)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		wantRoot, _, _ := Build(want)
		if res.Root != wantRoot || res.Delta != delta {
			t.Fatalf("%s: root ok %v, delta %+v (want %+v)", name, res.Root == wantRoot, res.Delta, delta)
		}
		for h := range res.Nodes {
			g[h] = res.Nodes[h]
		}
	}
	check("restore a file", []string{"docs", "a.txt"}, entryAt(target, "docs/a.txt"),
		[]FileEntry{fe("docs/a.txt", "old a", ModeRegular), fe("docs/b.txt", "b", ModeRegular), fe("tool.sh", "t", ModeExecutable)},
		Stats{0, 4})
	check("restore a directory that is missing", []string{"lib"}, entryAt(target, "lib"),
		[]FileEntry{fe("docs/a.txt", "a", ModeRegular), fe("docs/b.txt", "b", ModeRegular), fe("tool.sh", "t", ModeExecutable),
			fe("lib/x/y.txt", "y", ModeRegular), fe("lib/z.txt", "zz", ModeRegular)}, Stats{2, 3})
	check("restore into missing directories", []string{"new", "deep", "x"}, entryAt(target, "lib/x"),
		[]FileEntry{fe("docs/a.txt", "a", ModeRegular), fe("docs/b.txt", "b", ModeRegular), fe("tool.sh", "t", ModeExecutable),
			fe("new/deep/x/y.txt", "y", ModeRegular)}, Stats{1, 1})
	check("remove a directory", []string{"docs"}, nil, []FileEntry{fe("tool.sh", "t", ModeExecutable)}, Stats{-2, -2})
	check("replace a directory by a file", []string{"docs"}, entryAt(target, "lib/z.txt"),
		[]FileEntry{fe("docs", "zz", ModeRegular), fe("tool.sh", "t", ModeExecutable)}, Stats{-1, 0})
	if _, err := Graft(bg, g, root, []string{"tool.sh", "x"}, entryAt(target, "lib/z.txt")); pathx.KindOf(err) != pathx.KindFileDirConflict {
		t.Errorf("graft below a file: %v", err)
	}
	if _, err := Graft(bg, g, root, []string{"nope"}, nil); !errors.Is(err, ErrPathNotFound) {
		t.Errorf("remove a missing path: %v", err)
	}

	// The names a graft adds to a directory of base are checked against
	// the names there with the case rule: the last segment, and the
	// directories above it that base does not have.
	for _, tc := range []struct {
		segs []string
		e    *Entry
	}{
		{[]string{"DOCS"}, entryAt(target, "lib")},
		{[]string{"Docs", "c.txt"}, entryAt(target, "lib/z.txt")},
		{[]string{"Docs", "x", "c.txt"}, entryAt(target, "lib/z.txt")},
		{[]string{"docs", "A.txt"}, entryAt(target, "docs/a.txt")},
		{[]string{"TOOL.sh"}, entryAt(target, "lib/z.txt")},
	} {
		if _, err := Graft(bg, g, root, tc.segs, tc.e); pathx.KindOf(err) != pathx.KindCaseConflict {
			t.Errorf("graft %v: %v, want a case conflict", tc.segs, err)
		}
	}
	// A name that differs only in normalization has the same fold key.
	nfcRoot, nfcNodes, err := Build([]FileEntry{fe("caf\u00e9.txt", "nfc", ModeRegular)})
	if err != nil {
		t.Fatal(err)
	}
	for h, b := range nfcNodes {
		g[h] = b
	}
	if _, err := Graft(bg, g, nfcRoot, []string{"cafe\u0301.txt"}, entryAt(target, "lib/z.txt")); pathx.KindOf(err) != pathx.KindCaseConflict {
		t.Errorf("graft of an NFD name next to its NFC form: %v", err)
	}
	// The entries inside a grafted directory keep their stored bytes and
	// are not checked: this one (written by hand) holds X and x.
	sub := Encode(Node{Kind: KindDir, Entries: []Entry{fileEntry("X", "1", ModeRegular), fileEntry("x", "2", ModeRegular)}})
	g[HashOf(sub)] = sub
	res, err := Graft(bg, g, root, []string{"lib"}, &Entry{Type: TypeDir, Hash: HashOf(sub)})
	if err != nil || res.Delta != (Stats{2, 2}) {
		t.Fatalf("graft of a directory with names that conflict in case: %+v %v", res.Delta, err)
	}
}

// TestLookupListDirDiffMatchWalk checks Lookup, ListDir and Diff against
// Walk on random trees, with paged directories.
func TestLookupListDirDiffMatchWalk(t *testing.T) {
	for li, lim := range testLimits {
		r := rand.New(rand.NewSource(int64(200 + li)))
		for trial := 0; trial < 60; trial++ {
			ma, ra, na := randomBase(t, r, r.Intn(50), lim)
			mb := ma.clone()
			for k := r.Intn(8); k > 0; k-- {
				p := randomPath(r)
				switch r.Intn(3) {
				case 0:
					delete(mb, p)
				default:
					h, size := content(r, p)
					next := mb.clone()
					next[p] = FileEntry{Path: p, Hash: h, Size: size, Mode: []uint32{ModeRegular, ModeExecutable}[r.Intn(2)]}
					if _, _, err := build(next.list(), lim); err == nil {
						mb = next
					}
				}
			}
			if r.Intn(5) == 0 {
				mb, _, _ = randomBase(t, r, r.Intn(30), lim) // unrelated trees
			}
			rb, nb, err := build(mb.list(), lim)
			if err != nil {
				t.Fatal(err)
			}
			g := mergedWith(na, nb)
			// Lookup and ListDir against the walk.
			children := map[string][]Entry{}
			if err := Walk2(g, rb, lim, func(dir string, e Entry) {
				children[dir] = append(children[dir], e)
				if _, err := Lookup(bg, g, rb, split(joinPath(dir, e.Name))); err != nil {
					t.Fatalf("Lookup %q: %v", joinPath(dir, e.Name), err)
				}
			}); err != nil {
				t.Fatal(err)
			}
			for dir, want := range children {
				d, err := Lookup(bg, g, rb, split(dir))
				if err != nil {
					t.Fatal(err)
				}
				limit := 1 + r.Intn(5)
				var got []Entry
				after := ""
				for {
					page, more, err := ListDir(bg, g, d.Hash, after, limit)
					if err != nil {
						t.Fatal(err)
					}
					got = append(got, page...)
					if !more {
						break
					}
					after = page[len(page)-1].Name
				}
				if fmt.Sprint(got) != fmt.Sprint(want) {
					t.Fatalf("ListDir %q (limit %d):\n%v\nwant\n%v", dir, limit, got, want)
				}
			}
			// Diff against the file sets, in path order, with pagination.
			want := naiveDiff(ma, mb)
			limit := 1 + r.Intn(4)
			var got []DiffEntry
			var after []string
			for {
				page, more, err := Diff(bg, g, ra, rb, after, limit)
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, page...)
				if !more {
					break
				}
				after = split(page[len(page)-1].Path)
			}
			if fmtDiff(got) != fmtDiff(want) {
				t.Fatalf("limits %+v trial %d: Diff (limit %d):\n%s\nwant\n%s", lim, trial, limit, fmtDiff(got), fmtDiff(want))
			}
		}
	}
}

// Walk2 walks a tree built with the limits lim and reports every entry
// with the path of its directory.
func Walk2(g Getter, root contenthash.Hash, lim limits, fn func(dir string, e Entry)) error {
	w := walker{ctx: bg, g: g, lim: lim, onEntry: func(p string, e Entry) error {
		dir := ""
		if i := strings.LastIndexByte(p, '/'); i >= 0 {
			dir = p[:i]
		}
		fn(dir, e)
		return nil
	}}
	return w.dir("", root, true)
}

// pathLess is path order: segment by segment, each by its bytes.
func pathLess(a, b string) bool {
	sa, sb := strings.Split(a, "/"), strings.Split(b, "/")
	for i := 0; i < len(sa) && i < len(sb); i++ {
		if sa[i] != sb[i] {
			return sa[i] < sb[i]
		}
	}
	return len(sa) < len(sb)
}

func naiveDiff(a, b model) []DiffEntry {
	var out []DiffEntry
	for p, fa := range a {
		fb, ok := b[p]
		ea := Entry{Name: p, Type: TypeFile, Hash: fa.Hash, Size: fa.Size, Mode: fa.Mode}
		switch {
		case !ok:
			out = append(out, DiffEntry{Path: p, Kind: DiffDeleted, Old: &ea})
		case fa.Hash != fb.Hash || fa.Mode != fb.Mode:
			eb := Entry{Name: p, Type: TypeFile, Hash: fb.Hash, Size: fb.Size, Mode: fb.Mode}
			out = append(out, DiffEntry{Path: p, Kind: DiffModified, Old: &ea, New: &eb})
		}
	}
	for p, fb := range b {
		if _, ok := a[p]; !ok {
			eb := Entry{Name: p, Type: TypeFile, Hash: fb.Hash, Size: fb.Size, Mode: fb.Mode}
			out = append(out, DiffEntry{Path: p, Kind: DiffAdded, New: &eb})
		}
	}
	sort.Slice(out, func(i, j int) bool { return pathLess(out[i].Path, out[j].Path) })
	return out
}

func fmtDiff(ds []DiffEntry) string {
	var b strings.Builder
	for _, d := range ds {
		fmt.Fprintf(&b, "%s %s", d.Kind, d.Path)
		for _, e := range []*Entry{d.Old, d.New} {
			if e != nil {
				fmt.Fprintf(&b, " %s/%d/%d", e.Hash.Hex()[:8], e.Size, e.Mode)
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func TestDiffTypeChangesAndSkips(t *testing.T) {
	a, na, _ := Build([]FileEntry{fe("x", "file", ModeRegular), fe("y/z", "1", ModeRegular), fe("m", "m", ModeRegular)})
	b, nb, _ := Build([]FileEntry{fe("x/inner", "2", ModeRegular), fe("y", "file y", ModeRegular), fe("m", "m", ModeExecutable)})
	got, more, err := Diff(bg, mergedWith(na, nb), a, b, nil, 100)
	if err != nil || more {
		t.Fatal(err)
	}
	want := "modified m\ndeleted x\nadded x/inner\nadded y\ndeleted y/z\n"
	var s strings.Builder
	for _, d := range got {
		fmt.Fprintf(&s, "%s %s\n", d.Kind, d.Path)
	}
	if s.String() != want {
		t.Fatalf("diff:\n%swant\n%s", s.String(), want)
	}
	// Resuming after the deleted file x still lists what is below x.
	got, _, err = Diff(bg, mergedWith(na, nb), a, b, []string{"x"}, 1)
	if err != nil || len(got) != 1 || got[0].Path != "x/inner" {
		t.Fatalf("resume after x: %+v %v", got, err)
	}
	// Against the empty tree everything is added, without reading it.
	got, _, err = Diff(bg, mergedWith(na, nb), contenthash.Hash{}, b, nil, 100)
	if err != nil || len(got) != 3 || got[0].Kind != DiffAdded {
		t.Fatalf("diff from the empty tree: %+v %v", got, err)
	}

	// Two big flat directories that differ in one file: identical pages are
	// skipped without reading them.
	var big []FileEntry
	for i := 0; i < 6000; i++ {
		big = append(big, fe(fmt.Sprintf("f%05d", i), fmt.Sprint(i), ModeRegular))
	}
	ra, nodes, _ := Build(big)
	rb, nodes2, _ := Build(applyModel(big, put("f04000", "changed")))
	g := &countingGetter{m: mergedWith(nodes, nodes2)}
	got, _, err = Diff(bg, g, ra, rb, nil, 10)
	if err != nil || len(got) != 1 || got[0].Path != "f04000" {
		t.Fatalf("big diff: %+v %v", got, err)
	}
	if g.n.Load() > 4 {
		t.Errorf("big diff read %d nodes; want the two indexes and the two differing pages", g.n.Load())
	}
}

func TestLookupAndListDirReads(t *testing.T) {
	var big []FileEntry
	for i := 0; i < 5000; i++ {
		big = append(big, fe(fmt.Sprintf("d/f%05d", i), fmt.Sprint(i), ModeRegular))
	}
	root, nodes, _ := Build(big)
	g := &countingGetter{m: nodes}
	e, err := Lookup(bg, g, root, []string{"d", "f03333"})
	if err != nil || e.Type != TypeFile || g.n.Load() != 3 {
		t.Fatalf("Lookup in a paged directory: %+v %v, %d reads (want root, index, page)", e, err, g.n.Load())
	}
	d, _ := Lookup(bg, g, root, []string{"d"})
	g.n.Store(0)
	page, more, err := ListDir(bg, g, d.Hash, "f03000", 50)
	if err != nil || !more || len(page) != 50 || page[0].Name != "f03001" || g.n.Load() != 2 {
		t.Fatalf("ListDir after f03000: %d entries, more %v, %v, %d reads", len(page), more, err, g.n.Load())
	}
	for _, segs := range [][]string{{"d", "nope"}, {"d", "f00001", "below-a-file"}, {"x"}} {
		if _, err := Lookup(bg, g, root, segs); !errors.Is(err, ErrPathNotFound) {
			t.Errorf("Lookup %q: %v", segs, err)
		}
	}
	if e, err := Lookup(bg, g, root, nil); err != nil || e.Hash != root || e.Type != TypeDir {
		t.Errorf("Lookup of the root: %+v %v", e, err)
	}
	// LookupPath: the bytes as given first, then NFC.
	sroot, sg := storedTree(fileEntry("e\u0301", "nfd", ModeRegular), fileEntry("\u00e9x", "nfc", ModeRegular))
	if _, segs, err := LookupPath(bg, sg, sroot, "e\u0301", "\u00e9"); err != nil || segs[0] != "e\u0301" {
		t.Errorf("LookupPath of a stored NFD name: %q %v", segs, err)
	}
	if _, segs, err := LookupPath(bg, sg, sroot, "e\u0301x", "\u00e9x"); err != nil || segs[0] != "\u00e9x" {
		t.Errorf("LookupPath by NFC: %q %v", segs, err)
	}
	// Count reads the subtree.
	if files, bytes, err := Count(bg, g, root); err != nil || files != 5000 || bytes != int64(len(fmt.Sprint(0)))*10+90*2+900*3+4000*4 {
		t.Errorf("Count = %d %d %v", files, bytes, err)
	}
}

// TestReadersRejectInvalidStructure: request-path readers verify every
// node they read and the kinds of the references, and bound the depth.
func TestReadersRejectInvalidStructure(t *testing.T) {
	page := Encode(Node{Kind: KindDirPage, Entries: []Entry{fileEntry("a", "a", ModeRegular)}})
	ph := HashOf(page)
	dirPointsToPage, g := storedTree(Entry{Name: "d", Type: TypeDir, Hash: ph})
	g[ph] = page
	if _, err := Lookup(bg, g, dirPointsToPage, []string{"d", "a"}); !errors.Is(err, ErrInvalidTree) {
		t.Errorf("directory pointing to a dir_page: %v", err)
	}
	corrupt, cg := storedTree(fileEntry("a", "a", ModeRegular))
	cg[corrupt] = page // bytes that do not hash to the key
	if _, _, err := ListDir(bg, cg, corrupt, "", 10); !errors.Is(err, ErrHashMismatch) {
		t.Errorf("node with the wrong bytes: %v", err)
	}
	if _, err := Lookup(bg, g, dirPointsToPage, make([]string, MaxDepth+1)); !errors.Is(err, ErrTooDeep) {
		t.Errorf("too many segments: %v", err)
	}
}

// TestApplyMultiLevelIndex: with reduced limits a directory gets several
// index levels; a replace rewrites one page and the indexes above it, a
// delete everything after it.
func TestApplyMultiLevelIndex(t *testing.T) {
	lim := limits{maxItems: 4, maxBytes: MaxNodeBytes}
	var files []FileEntry
	for i := 0; i < 70; i++ { // 18 pages -> 5 level-1 indexes -> 2 level-2 -> top
		files = append(files, fe(fmt.Sprintf("f%03d", i), fmt.Sprint(i), ModeRegular))
	}
	root, nodes, err := build(files, lim)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		c     Change
		nodes int
	}{
		{put("f033", "new"), 4}, // page, level-1, level-2, top
		{put("f000", "new"), 4}, // the first page
		{put("f069", "new"), 4}, // the last page
		{put("f067", "x"), 4},   // the last full page
		// f066 is on page 17 of 18 (f064-f067): the last two pages change
		// (f068 moves up), plus their level-1 and level-2 index and the top.
		{del("f066"), 2 + 1 + 1 + 1},
		// f001 shifts every page: 18 pages, 5 + 2 indexes and the top.
		{del("f001"), 18 + 5 + 2 + 1},
		{del("f069"), 4},                   // the last page loses an entry
		{put("f0335", "inserted"), 0},      // a new name: the whole directory
		{put("f\u00e9", "non-ASCII"), 0},   // a new name after every other
		{put("e", "before every name"), 0}, // a new name before every other
	} {
		g := &countingGetter{m: nodes}
		res, err := apply(bg, g, root, []Change{tc.c}, lim)
		if err != nil {
			t.Fatal(err)
		}
		want, wantNodes, err := build(applyModel(files, tc.c), lim)
		if err != nil {
			t.Fatal(err)
		}
		if res.Root != want {
			t.Fatalf("%+v: root differs from Build", tc.c)
		}
		newNodes := 0
		for h := range wantNodes {
			if _, ok := nodes[h]; !ok {
				newNodes++
			}
		}
		if len(res.Nodes) != newNodes {
			t.Errorf("%+v: %d nodes returned, the new tree has %d nodes the base does not have", tc.c, len(res.Nodes), newNodes)
		}
		if tc.nodes > 0 && newNodes != tc.nodes {
			t.Errorf("%+v: %d new nodes, want %d", tc.c, newNodes, tc.nodes)
		}
	}
}

func hundredK(dirs int) []FileEntry {
	files := make([]FileEntry, 0, 100_000)
	for i := 0; i < 100_000; i++ {
		p := fmt.Sprintf("f%06d", i)
		if dirs > 1 {
			p = fmt.Sprintf("d%03d/%s", i%dirs, p)
		}
		files = append(files, fe(p, p, ModeRegular))
	}
	return files
}

// TestApply100kWritesOnlyThePath: in a tree of 100,000 files, replacing one
// file writes only the nodes on its path and reads about as many.
func TestApply100kWritesOnlyThePath(t *testing.T) {
	if testing.Short() {
		t.Skip("builds two trees of 100,000 files")
	}
	for _, tc := range []struct {
		name         string
		dirs         int
		nodes, reads int64
	}{
		{"100 directories of 1000 files", 100, 2, 2}, // the root and the directory
		{"one directory of 100000 files", 1, 2, 2},   // the root index and one page
	} {
		files := hundredK(tc.dirs)
		root, nodes, err := Build(files)
		if err != nil {
			t.Fatal(err)
		}
		g := &countingGetter{m: nodes}
		c := put(files[54321].Path, "changed")
		res, err := Apply(bg, g, root, []Change{c})
		if err != nil {
			t.Fatal(err)
		}
		want, _, _ := Build(applyModel(files, c))
		if res.Root != want || int64(len(res.Nodes)) != tc.nodes || g.n.Load() != tc.reads {
			t.Errorf("%s: root ok %v, %d nodes written (want %d), %d nodes read (want %d)", tc.name, res.Root == want,
				len(res.Nodes), tc.nodes, g.n.Load(), tc.reads)
		}
	}
}

// BenchmarkApplyOneFileIn100k measures replacing one file in a tree of
// 100,000 files (nodes/op: nodes written; reads/op: nodes read).
func BenchmarkApplyOneFileIn100k(b *testing.B) {
	for _, shape := range []struct {
		name string
		dirs int
	}{{"100x1000", 100}, {"flat", 1}} {
		files := hundredK(shape.dirs)
		root, nodes, err := Build(files)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(shape.name, func(b *testing.B) {
			g := &countingGetter{m: nodes}
			var written int
			for i := 0; i < b.N; i++ {
				res, err := Apply(bg, g, root, []Change{put(files[54321].Path, fmt.Sprint("v", i))})
				if err != nil {
					b.Fatal(err)
				}
				written = len(res.Nodes)
			}
			b.ReportMetric(float64(written), "nodes/op")
			b.ReportMetric(float64(g.n.Load())/float64(b.N), "reads/op")
		})
	}
}
