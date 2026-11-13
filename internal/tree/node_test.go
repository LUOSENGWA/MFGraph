package tree_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/pathx"
	"github.com/LUOSENGWA/MFGraph/internal/tree"
)

var h0 = contenthash.SumBytes(nil)

func TestEncodeStringEscaping(t *testing.T) {
	// Names never contain these characters (pathx rejects them), but the
	// encoder must still be a correct JCS string encoder.
	var name strings.Builder
	for c := 0; c < 0x20; c++ {
		name.WriteByte(byte(c))
	}
	name.WriteString("\"\\/<>&\x7fé🚀 ")
	n := tree.Node{Kind: tree.KindDir, Entries: []tree.Entry{{Name: name.String(), Type: tree.TypeDir, Hash: h0}}}
	got := string(tree.Encode(n))
	want := `{"entries":[{"hash":"` + h0.String() + `","name":"` +
		`\u0000\u0001\u0002\u0003\u0004\u0005\u0006\u0007\b\t\n\u000b\f\r\u000e\u000f` +
		`\u0010\u0011\u0012\u0013\u0014\u0015\u0016\u0017\u0018\u0019\u001a\u001b\u001c\u001d\u001e\u001f` +
		`\"\\/<>&` + "\x7fé🚀 " + `","type":"dir"}],"format":1,"kind":"dir"}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestEncodeKeyOrderAndShape(t *testing.T) {
	file := tree.Entry{Name: "a.txt", Type: tree.TypeFile, Hash: h0, Size: 0, Mode: tree.ModeExecutable}
	got := string(tree.Encode(tree.Node{Kind: tree.KindDirPage, Entries: []tree.Entry{file}}))
	want := `{"entries":[{"hash":"` + h0.String() + `","mode":493,"name":"a.txt","size":0,"type":"file"}],"format":1,"kind":"dir_page"}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	idx := string(tree.Encode(tree.Node{Kind: tree.KindDirIndex, Pages: []tree.PageRef{{Count: 3, First: "a", Hash: h0}}}))
	if idx != `{"format":1,"kind":"dir_index","pages":[{"count":3,"first":"a","hash":"`+h0.String()+`"}]}` {
		t.Fatalf("index encoding: %s", idx)
	}
	if e := string(tree.Encode(tree.Node{Kind: tree.KindDir})); e != `{"entries":[],"format":1,"kind":"dir"}` {
		t.Fatalf("empty dir: %s", e)
	}
}

func TestValidateRejects(t *testing.T) {
	f := func(name string) tree.Entry {
		return tree.Entry{Name: name, Type: tree.TypeFile, Hash: h0, Mode: tree.ModeRegular}
	}
	cases := map[string]tree.Node{
		"unsorted":         {Kind: tree.KindDir, Entries: []tree.Entry{f("b"), f("a")}},
		"duplicate":        {Kind: tree.KindDir, Entries: []tree.Entry{f("a"), f("a")}},
		"c1-control":       {Kind: tree.KindDir, Entries: []tree.Entry{f("a\u0085")}},
		"bidi-override":    {Kind: tree.KindDir, Entries: []tree.Entry{f("a\u202eb")}},
		"zero-width":       {Kind: tree.KindDir, Entries: []tree.Entry{f("a\u200b")}},
		"slash":            {Kind: tree.KindDir, Entries: []tree.Entry{f("a/b")}},
		"index-first-bidi": {Kind: tree.KindDirIndex, Pages: []tree.PageRef{{Count: 1, First: "a\u2066", Hash: h0}}},
		"bad-mode":         {Kind: tree.KindDir, Entries: []tree.Entry{{Name: "a", Type: tree.TypeFile, Hash: h0, Mode: 0o600}}},
		"dir-with-size":    {Kind: tree.KindDir, Entries: []tree.Entry{{Name: "a", Type: tree.TypeDir, Hash: h0, Size: 1}}},
		"zero-hash":        {Kind: tree.KindDir, Entries: []tree.Entry{{Name: "a", Type: tree.TypeFile, Mode: tree.ModeRegular}}},
		"empty-page":       {Kind: tree.KindDirPage},
		"empty-index":      {Kind: tree.KindDirIndex},
		"unknown-kind":     {Kind: "file"},
		"too-big-size":     {Kind: tree.KindDir, Entries: []tree.Entry{{Name: "a", Type: tree.TypeFile, Hash: h0, Mode: tree.ModeRegular, Size: tree.MaxSafeInteger + 1}}},
		"index-zero-count": {Kind: tree.KindDirIndex, Pages: []tree.PageRef{{Count: 0, First: "a", Hash: h0}}},
	}
	for name, n := range cases {
		if err := n.Validate(); !errors.Is(err, tree.ErrInvalidNode) {
			t.Errorf("%s: Validate = %v", name, err)
		}
	}
	tooMany := tree.Node{Kind: tree.KindDir}
	for i := 0; i <= tree.MaxEntriesPerNode; i++ {
		tooMany.Entries = append(tooMany.Entries, f(strings.Repeat("a", 1+i/26)+string(rune('a'+i%26))))
	}
	if err := tooMany.Validate(); !errors.Is(err, tree.ErrInvalidNode) {
		t.Errorf("1025 entries: Validate = %v", err)
	}
}

// TestValidateReadTimeRulesOnly: names that only the write-time rules reject
// (not NFC, case-fold conflicts, unassigned code points) are readable.
func TestValidateReadTimeRulesOnly(t *testing.T) {
	f := func(name string) tree.Entry {
		return tree.Entry{Name: name, Type: tree.TypeFile, Hash: h0, Mode: tree.ModeRegular}
	}
	for name, n := range map[string]tree.Node{
		"nfd-name":      {Kind: tree.KindDir, Entries: []tree.Entry{f("cafe\u0301")}},
		"case-conflict": {Kind: tree.KindDir, Entries: []tree.Entry{f("A"), f("a")}},
		"unassigned":    {Kind: tree.KindDir, Entries: []tree.Entry{f("x\u0378")}},
		"noncharacter":  {Kind: tree.KindDir, Entries: []tree.Entry{f("x\uffff")}},
		"zwj-zwnj":      {Kind: tree.KindDir, Entries: []tree.Entry{f("a\u200cb\u200dc")}},
		"index-nfd":     {Kind: tree.KindDirIndex, Pages: []tree.PageRef{{Count: 1, First: "cafe\u0301", Hash: h0}, {Count: 1, First: "d", Hash: h0}}},
	} {
		if err := n.Validate(); err != nil {
			t.Errorf("%s: Validate = %v, want nil", name, err)
		}
		if _, err := tree.Decode(tree.Encode(n)); err != nil {
			t.Errorf("%s: Decode = %v, want nil", name, err)
		}
	}
}

func TestDecodeVerifiedHashMismatch(t *testing.T) {
	b := tree.Encode(tree.Node{Kind: tree.KindDir})
	if _, err := tree.DecodeVerified(b, h0); !errors.Is(err, tree.ErrHashMismatch) {
		t.Fatalf("err = %v", err)
	}
	if _, err := tree.DecodeVerified(b, tree.HashOf(b)); err != nil {
		t.Fatal(err)
	}
}

func TestBuildRejectsInvalidEntries(t *testing.T) {
	for name, f := range map[string]tree.FileEntry{
		"zero-hash":     {Path: "a", Mode: tree.ModeRegular},
		"negative-size": {Path: "a", Hash: h0, Size: -1, Mode: tree.ModeRegular},
		"mode-0":        {Path: "a", Hash: h0},
		"mode-0644-ok?": {Path: "a", Hash: h0, Mode: 0o600},
	} {
		if _, _, err := tree.Build([]tree.FileEntry{f}); !errors.Is(err, tree.ErrInvalidEntry) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	_, _, err := tree.Build([]tree.FileEntry{{Path: "a//b", Hash: h0, Mode: tree.ModeRegular}})
	if pathx.KindOf(err) != pathx.KindEmptySegment || !errors.Is(err, pathx.ErrInvalidPath) {
		t.Errorf("invalid path: err = %v", err)
	}
}

func TestBuildConflictReportsFullPath(t *testing.T) {
	_, _, err := tree.Build([]tree.FileEntry{
		{Path: "docs/Plan.dwg", Hash: h0, Mode: tree.ModeRegular},
		{Path: "docs/plan.dwg", Hash: h0, Mode: tree.ModeRegular},
	})
	var pe *pathx.Error
	if !errors.As(err, &pe) || pe.Kind != pathx.KindCaseConflict || pe.Input != "docs/plan.dwg" {
		t.Fatalf("err = %#v", err)
	}
}

func TestEmptyRoot(t *testing.T) {
	root, nodes, err := tree.Build(nil)
	if err != nil || len(nodes) != 1 || string(nodes[root]) != `{"entries":[],"format":1,"kind":"dir"}` {
		t.Fatalf("empty build: %v %q", err, nodes[root])
	}
}

func TestWalkRejectsBrokenTrees(t *testing.T) {
	ctx := context.Background()
	noop := func(string, tree.Entry) error { return nil }
	empty := tree.Encode(tree.Node{Kind: tree.KindDir})
	emptyHash := tree.HashOf(empty)

	// A non-root directory pointing at an empty node.
	parent := tree.Encode(tree.Node{Kind: tree.KindDir, Entries: []tree.Entry{{Name: "d", Type: tree.TypeDir, Hash: emptyHash}}})
	g := tree.MapGetter{emptyHash: empty, tree.HashOf(parent): parent}
	if err := tree.Walk(ctx, g, tree.HashOf(parent), noop); !errors.Is(err, tree.ErrInvalidTree) {
		t.Errorf("empty subdirectory: err = %v", err)
	}
	// The root may be empty.
	if err := tree.Walk(ctx, g, emptyHash, noop); err != nil {
		t.Errorf("empty root: %v", err)
	}
	// Bytes that do not match the requested hash.
	bad := tree.MapGetter{emptyHash: parent}
	if err := tree.Walk(ctx, bad, emptyHash, noop); !errors.Is(err, tree.ErrHashMismatch) {
		t.Errorf("hash mismatch: err = %v", err)
	}
	// Missing node.
	if err := tree.Walk(ctx, tree.MapGetter{}, emptyHash, noop); !errors.Is(err, tree.ErrNodeNotFound) {
		t.Errorf("missing node: err = %v", err)
	}
	// A directory entry pointing at a dir_page.
	page := tree.Encode(tree.Node{Kind: tree.KindDirPage, Entries: []tree.Entry{{Name: "f", Type: tree.TypeFile, Hash: h0, Mode: tree.ModeRegular}}})
	top := tree.Encode(tree.Node{Kind: tree.KindDir, Entries: []tree.Entry{{Name: "d", Type: tree.TypeDir, Hash: tree.HashOf(page)}}})
	g2 := tree.MapGetter{tree.HashOf(page): page, tree.HashOf(top): top}
	if err := tree.Walk(ctx, g2, tree.HashOf(top), noop); !errors.Is(err, tree.ErrInvalidTree) {
		t.Errorf("dir -> dir_page: err = %v", err)
	}
	// An index over a single small page is not the canonical split.
	idx := tree.Encode(tree.Node{Kind: tree.KindDirIndex, Pages: []tree.PageRef{{Count: 1, First: "f", Hash: tree.HashOf(page)}}})
	g3 := tree.MapGetter{tree.HashOf(page): page, tree.HashOf(idx): idx}
	if err := tree.Walk(ctx, g3, tree.HashOf(idx), noop); !errors.Is(err, tree.ErrInvalidTree) {
		t.Errorf("non-canonical index: err = %v", err)
	}
	// A reference whose count does not match.
	idx2 := tree.Encode(tree.Node{Kind: tree.KindDirIndex, Pages: []tree.PageRef{{Count: 2, First: "f", Hash: tree.HashOf(page)}}})
	g4 := tree.MapGetter{tree.HashOf(page): page, tree.HashOf(idx2): idx2}
	if err := tree.Walk(ctx, g4, tree.HashOf(idx2), noop); !errors.Is(err, tree.ErrInvalidTree) {
		t.Errorf("count mismatch: err = %v", err)
	}
	// Cancellation is honored.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := tree.Walk(cctx, g, emptyHash, noop); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: err = %v", err)
	}
}

func TestModeFromPerm(t *testing.T) {
	for perm, want := range map[uint32]uint32{0o644: 420, 0o600: 420, 0o755: 493, 0o744: 493, 0o701: 493, 0o610: 493, 0: 420} {
		if got := tree.ModeFromPerm(fsMode(perm)); got != want {
			t.Errorf("ModeFromPerm(%o) = %d, want %d", perm, got, want)
		}
	}
}
