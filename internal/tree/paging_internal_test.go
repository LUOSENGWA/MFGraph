package tree

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
)

// checkApply applies cs to the tree of files built with lim and checks that
// the result is the tree Build gives for the resulting files, and that Apply
// returns exactly the nodes of the new tree that the base does not have.
func checkApply(t *testing.T, files []FileEntry, lim limits, cs ...Change) {
	t.Helper()
	root, nodes, err := build(files, lim)
	if err != nil {
		t.Fatal(err)
	}
	res, err := apply(bg, MapGetter(nodes), root, cs, lim)
	if err != nil {
		t.Fatalf("%v: %v", cs, err)
	}
	want, wantNodes, err := build(applyModel(files, cs...), lim)
	if err != nil {
		t.Fatal(err)
	}
	if res.Root != want {
		t.Fatalf("%v: the root differs from Build", describe(cs))
	}
	fresh := 0
	for h := range wantNodes {
		if _, ok := nodes[h]; !ok {
			fresh++
			if _, ok := res.Nodes[h]; !ok {
				t.Fatalf("%v: a new node is missing from the result", describe(cs))
			}
		}
	}
	if len(res.Nodes) != fresh {
		t.Fatalf("%v: %d nodes returned, the new tree has %d nodes the base does not have", describe(cs), len(res.Nodes), fresh)
	}
}

func describe(cs []Change) string {
	var parts []string
	for _, c := range cs {
		if c.Delete {
			parts = append(parts, "delete "+c.Path)
		} else {
			parts = append(parts, fmt.Sprintf("put %s (%d bytes)", c.Path, c.Size))
		}
	}
	return strings.Join(parts, ", ")
}

// pageCounts returns the number of entries of every page of the directory
// at the root of the tree of files.
func pageCounts(t *testing.T, files []FileEntry, lim limits) []int64 {
	t.Helper()
	root, nodes, err := build(files, lim)
	if err != nil {
		t.Fatal(err)
	}
	n, err := DecodeVerified(nodes[root], root)
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind != KindDirIndex {
		t.Fatalf("the root is a %s, not paged", n.Kind)
	}
	var counts []int64
	for _, p := range n.Pages {
		counts = append(counts, p.Count)
	}
	return counts
}

// TestApplyPagesClosedBySize: when a page was closed because the first
// entry of the next page did not fit, removing that entry, or replacing it
// with a shorter one, can let the next entry fit into the page, which then
// changes too.
func TestApplyPagesClosedBySize(t *testing.T) {
	short := len(appendEntry(nil, fileEntry("a0", "x", ModeRegular)))
	// Five short entries fill a page exactly; a page has room for ten.
	lim := limits{maxItems: 10, maxBytes: overheadDirPage + 5*short + 4}
	var files []FileEntry
	for i := 0; i < 4; i++ {
		files = append(files, fe(fmt.Sprintf("a%d", i), "x", ModeRegular))
	}
	long := FileEntry{Path: "b0", Hash: contenthash.SumBytes([]byte("b0")), Size: 1_000_000_000_000, Mode: ModeRegular}
	files = append(files, long)
	for i := 0; i < 10; i++ {
		files = append(files, fe(fmt.Sprintf("c%d", i), "x", ModeRegular))
	}
	// The first page holds a0..a3: b0, twelve digits longer, does not fit.
	if got := pageCounts(t, files, lim); got[0] != 4 {
		t.Fatalf("pages %v: the first page does not end before b0", got)
	}
	shorter := Change{Path: "b0", Hash: contenthash.SumBytes([]byte("b0 again")), Size: 1}
	for _, cs := range [][]Change{
		{del("b0")},
		{shorter},
		{del("b0"), del("c0")},
		{shorter, del("c5")},
		{del("b0"), put("c9", "a longer content")},
		{del("a3")},
		{put("a3", "a longer content")},
		{del("c9")},
	} {
		checkApply(t, files, lim, cs...)
	}
}

// TestApplyVariableSizesMatchesBuild: random flat directories whose entries
// differ much in size (names of 1 to 120 bytes, sizes of up to 16 digits),
// with limits under which pages close by size, by count, or both, and
// random batches of replacements, removals and new names.
func TestApplyVariableSizesMatchesBuild(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	letters := []string{"a", "b", "c", "\u00e9", "\u6587", "\U0001F680", "\"", "_"}
	name := func() string {
		var b strings.Builder
		b.WriteString(letters[r.Intn(len(letters))])
		for n := r.Intn(40); n > 0; n-- {
			b.WriteString(letters[r.Intn(len(letters))])
		}
		s := b.String()
		if len(s) > 120 {
			s = s[:120]
			for !utf8.ValidString(s) {
				s = s[:len(s)-1]
			}
		}
		return s
	}
	size := func() int64 {
		switch r.Intn(3) {
		case 0:
			return int64(r.Intn(10))
		case 1:
			return r.Int63n(1_000_000)
		}
		return r.Int63n(MaxSafeInteger)
	}
	entry := func(p string) FileEntry {
		return FileEntry{Path: p, Hash: contenthash.SumBytes([]byte(fmt.Sprint(p, r.Int()))), Size: size(), Mode: ModeRegular}
	}
	lims := []limits{
		{maxItems: 8, maxBytes: 900},
		{maxItems: 64, maxBytes: 700},
		{maxItems: 5, maxBytes: 2000},
		{maxItems: 4, maxBytes: 600},
	}
	trials := 300
	if testing.Short() {
		trials = 60
	}
	for li, lim := range lims {
		for trial := 0; trial < trials; trial++ {
			files := map[string]FileEntry{}
			for n := 10 + r.Intn(50); len(files) < n; {
				p := name()
				if _, _, err := build(append(mapList(files), entry(p)), lim); err == nil {
					files[p] = entry(p)
				}
			}
			list := mapList(files)
			sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
			var cs []Change
			used := map[string]bool{}
			for k := 1 + r.Intn(4); k > 0; k-- {
				f := list[r.Intn(len(list))]
				if used[f.Path] {
					continue
				}
				used[f.Path] = true
				switch r.Intn(5) {
				case 0, 1:
					cs = append(cs, del(f.Path))
				case 2, 3:
					e := entry(f.Path)
					cs = append(cs, Change{Path: f.Path, Hash: e.Hash, Size: e.Size})
				default:
					p := name()
					if _, ok := files[p]; ok || used[p] {
						continue
					}
					if _, _, err := build(applyModel(list, append(cs, Change{Path: p, Hash: contenthash.SumBytes([]byte(p)), Size: 1})...), lim); err != nil {
						continue // a case conflict
					}
					used[p] = true
					cs = append(cs, Change{Path: p, Hash: contenthash.SumBytes([]byte(p)), Size: 1})
				}
			}
			if len(cs) == 0 {
				continue
			}
			t.Run(fmt.Sprintf("limits%d/trial%d", li, trial), func(t *testing.T) {
				checkApply(t, list, lim, cs...)
			})
		}
	}
}

func mapList(m map[string]FileEntry) []FileEntry {
	out := make([]FileEntry, 0, len(m))
	for _, f := range m {
		out = append(out, f)
	}
	return out
}
