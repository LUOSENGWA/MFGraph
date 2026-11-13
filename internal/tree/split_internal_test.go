package tree

import (
	"context"
	"fmt"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
)

// These tests exercise the splitting code paths with reduced limits. The
// frozen limits make the 1 MiB rule unreachable for valid names (a node with
// 1024 maximal entries encodes to about 650 KB; see the
// "longest-names-1024-entries-still-one-node" vector), so the byte rule can
// only be tested this way.

func genFiles(dir string, n int) []FileEntry {
	files := make([]FileEntry, n)
	for i := range files {
		p := fmt.Sprintf("f%05d", i)
		if dir != "" {
			p = dir + "/" + p
		}
		files[i] = FileEntry{Path: p, Hash: contenthash.SumBytes([]byte(p)), Size: int64(len(p)), Mode: ModeRegular}
	}
	return files
}

type shape struct {
	kinds  []Kind
	counts []int // entries (dir/dir_page) or references (dir_index) per node
	sizes  []int
}

func walkShape(t *testing.T, lim limits, root contenthash.Hash, nodes map[contenthash.Hash][]byte) shape {
	t.Helper()
	var s shape
	w := walker{ctx: context.Background(), g: MapGetter(nodes), lim: lim, onNode: func(ni NodeInfo) error {
		if ni.Dir != "" {
			return nil
		}
		s.kinds = append(s.kinds, ni.Node.Kind)
		if ni.Node.Kind == KindDirIndex {
			s.counts = append(s.counts, len(ni.Node.Pages))
		} else {
			s.counts = append(s.counts, len(ni.Node.Entries))
		}
		s.sizes = append(s.sizes, len(ni.Canonical))
		return nil
	}}
	if err := w.dir("", root, true); err != nil {
		t.Fatalf("walk with reduced limits: %v", err)
	}
	return s
}

func TestSplitByCountMultiLevel(t *testing.T) {
	lim := limits{maxItems: 4, maxBytes: MaxNodeBytes}
	// 17 entries -> pages 4,4,4,4,1 (5 pages > 4) -> level-1 indexes [4],[1]
	// -> top index with 2 references.
	root, nodes, err := build(genFiles("", 17), lim)
	if err != nil {
		t.Fatal(err)
	}
	s := walkShape(t, lim, root, nodes)
	wantKinds := []Kind{KindDirIndex, KindDirIndex, KindDirPage, KindDirPage, KindDirPage, KindDirPage, KindDirIndex, KindDirPage}
	wantCounts := []int{2, 4, 4, 4, 4, 4, 1, 1}
	if fmt.Sprint(s.kinds) != fmt.Sprint(wantKinds) || fmt.Sprint(s.counts) != fmt.Sprint(wantCounts) {
		t.Fatalf("shape kinds=%v counts=%v, want %v %v", s.kinds, s.counts, wantKinds, wantCounts)
	}
	top, err := Decode(nodes[root])
	if err != nil {
		t.Fatal(err)
	}
	if top.Pages[0].Count != 16 || top.Pages[1].Count != 1 || top.Pages[0].First != "f00000" || top.Pages[1].First != "f00016" {
		t.Fatalf("top references = %+v (count must be the number of entries below)", top.Pages)
	}
	// The same tree is rejected by the frozen-limit walker: a 17-entry
	// directory must be a single dir node.
	if err := Walk(context.Background(), MapGetter(nodes), root, func(string, Entry) error { return nil }); err == nil {
		t.Fatal("frozen-limit walk accepted a non-canonical split")
	}
}

func TestSplitByBytesGreedy(t *testing.T) {
	entrySize := len(appendEntry(nil, Entry{Name: "f00000", Type: TypeFile, Hash: contenthash.SumBytes(nil), Size: 6, Mode: ModeRegular}))
	// Room for exactly three entries per page.
	maxBytes := overheadDirPage + 3*entrySize + 2
	lim := limits{maxItems: MaxEntriesPerNode, maxBytes: maxBytes}
	root, nodes, err := build(genFiles("", 10), lim)
	if err != nil {
		t.Fatal(err)
	}
	s := walkShape(t, lim, root, nodes)
	// Pages hold 3,3,3,1 entries. An index over 4 references would also exceed
	// the byte limit, so the references are packed into indexes [3],[1] under
	// a top index with 2 references.
	wantKinds := []Kind{KindDirIndex, KindDirIndex, KindDirPage, KindDirPage, KindDirPage, KindDirIndex, KindDirPage}
	if fmt.Sprint(s.kinds) != fmt.Sprint(wantKinds) || fmt.Sprint(s.counts) != fmt.Sprint([]int{2, 3, 3, 3, 3, 1, 1}) {
		t.Fatalf("kinds=%v counts=%v", s.kinds, s.counts)
	}
	for i := range s.kinds {
		if s.sizes[i] > maxBytes {
			t.Fatalf("node %d is %d bytes > %d", i, s.sizes[i], maxBytes)
		}
	}
	if s.sizes[2] != maxBytes {
		t.Fatalf("a full page must be exactly at the limit: %d vs %d", s.sizes[2], maxBytes)
	}
}

func TestGreedyBoundaries(t *testing.T) {
	cases := []struct {
		sizes    []int
		overhead int
		lim      limits
		want     string
	}{
		{[]int{10, 10, 10}, 2, limits{3, 100}, "[{0 3}]"},
		{[]int{10, 10, 10}, 2, limits{2, 100}, "[{0 2} {2 3}]"},
		{[]int{10, 10, 10}, 2, limits{10, 23}, "[{0 2} {2 3}]"}, // 2+10+1+10 = 23 fits exactly
		{[]int{10, 10, 10}, 2, limits{10, 22}, "[{0 1} {1 2} {2 3}]"},
		{[]int{5}, 2, limits{1, 7}, "[{0 1}]"},
		{nil, 2, limits{1, 7}, "[{0 0}]"},
	}
	for _, c := range cases {
		got, err := greedy(c.sizes, c.overhead, c.lim)
		if err != nil || fmt.Sprint(got) != c.want {
			t.Errorf("greedy(%v, %d, %+v) = %v %v, want %s", c.sizes, c.overhead, c.lim, got, err, c.want)
		}
	}
	if _, err := greedy([]int{50}, 2, limits{10, 20}); err == nil {
		t.Error("an item larger than the node limit must be an error")
	}
}

func TestOverheads(t *testing.T) {
	if overheadDir != 38 || overheadDirPage != 43 || overheadDirIndex != 42 {
		t.Fatalf("overheads = %d %d %d", overheadDir, overheadDirPage, overheadDirIndex)
	}
}
