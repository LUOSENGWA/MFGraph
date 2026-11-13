package tree_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/pathx"
	"github.com/LUOSENGWA/MFGraph/internal/tree"
)

type vecFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    uint32 `json:"mode"`
}

type vecGen struct {
	Dir          string `json:"dir"`
	Prefix       string `json:"prefix"`
	PrefixRepeat int    `json:"prefix_repeat"`
	Digits       int    `json:"digits"`
	Start        int    `json:"start"`
	Count        int    `json:"count"`
}

type vecNode struct {
	Dir       string  `json:"dir"`
	Kind      string  `json:"kind"`
	Hash      string  `json:"hash"`
	Size      int     `json:"size"`
	Canonical *string `json:"canonical"`
	Position  *int    `json:"position"`
}

type vecCase struct {
	Name       string    `json:"name"`
	Heavy      bool      `json:"heavy"`
	ListNodes  []int     `json:"list_nodes"`
	Files      []vecFile `json:"files"`
	Generate   []vecGen  `json:"generate"`
	Root       string    `json:"root"`
	NodeCount  int       `json:"node_count"`
	WalkLength int       `json:"walk_length"`
	Nodes      []vecNode `json:"nodes"`
	Error      string    `json:"error"`
}

type vecDoc struct {
	Schema        string         `json:"schema"`
	ReviewStatus  string         `json:"review_status"`
	Description   string         `json:"description"`
	Inputs        string         `json:"inputs"`
	Limits        map[string]int `json:"limits"`
	Cases         []vecCase      `json:"cases"`
	DecodeInvalid []struct {
		Name  string `json:"name"`
		Bytes string `json:"bytes"`
	} `json:"decode_invalid"`
	DecodeValidBuildRejects []struct {
		Name      string `json:"name"`
		Bytes     string `json:"bytes"`
		WriteRule string `json:"write_rule"`
	} `json:"decode_valid_build_rejects"`
}

func loadTreeVectors(t testing.TB) vecDoc {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "tree.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v vecDoc
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode tree.json: %v", err)
	}
	if v.Limits["max_entries_per_node"] != tree.MaxEntriesPerNode || v.Limits["max_node_bytes"] != tree.MaxNodeBytes {
		t.Fatalf("limits in tree.json %v do not match the implementation", v.Limits)
	}
	return v
}

// caseFiles expands the inputs of a case into Build input (test-side
// re-implementation of the documented generator rule).
func caseFiles(c vecCase) []tree.FileEntry {
	var files []tree.FileEntry
	for _, f := range c.Files {
		mode := f.Mode
		if mode == 0 {
			mode = tree.ModeRegular
		}
		files = append(files, tree.FileEntry{Path: f.Path, Hash: contenthash.SumBytes([]byte(f.Content)),
			Size: int64(len(f.Content)), Mode: mode})
	}
	for _, g := range c.Generate {
		rep := g.PrefixRepeat
		if rep == 0 {
			rep = 1
		}
		prefix := strings.Repeat(g.Prefix, rep)
		for i := g.Start; i < g.Start+g.Count; i++ {
			p := fmt.Sprintf("%s%0*d", prefix, g.Digits, i)
			if g.Dir != "" {
				p = g.Dir + "/" + p
			}
			files = append(files, tree.FileEntry{Path: p, Hash: contenthash.SumBytes([]byte(p)), Size: int64(len(p)), Mode: tree.ModeRegular})
		}
	}
	return files
}

func heavyEnabled() bool { return os.Getenv("MFGRAPH_HEAVY_VECTORS") == "1" }

func TestTreeVectors(t *testing.T) {
	v := loadTreeVectors(t)
	ctx := context.Background()
	for _, c := range v.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.Heavy && !heavyEnabled() {
				t.Skip("heavy vector; set MFGRAPH_HEAVY_VECTORS=1")
			}
			files := caseFiles(c)
			root, nodes, err := tree.Build(files)
			if c.Error != "" {
				if got := pathx.KindOf(err); string(got) != c.Error {
					t.Fatalf("Build error = %v (kind %q), want kind %s", err, got, c.Error)
				}
				return
			}
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if root.String() != c.Root {
				t.Errorf("root = %s, want %s", root, c.Root)
			}
			if len(nodes) != c.NodeCount {
				t.Errorf("node count = %d, want %d", len(nodes), c.NodeCount)
			}
			var walked []tree.NodeInfo
			if err := tree.WalkNodes(ctx, tree.MapGetter(nodes), root, func(ni tree.NodeInfo) error {
				walked = append(walked, ni)
				return nil
			}); err != nil {
				t.Fatalf("WalkNodes: %v", err)
			}
			want := c.Nodes
			if c.ListNodes != nil {
				if len(walked) != c.WalkLength {
					t.Fatalf("walk length = %d, want %d", len(walked), c.WalkLength)
				}
			} else if len(walked) != len(want) {
				t.Fatalf("walked %d nodes, vector lists %d", len(walked), len(want))
			}
			for i, wn := range want {
				pos := i
				if wn.Position != nil {
					pos = *wn.Position
				}
				got := walked[pos]
				if got.Dir != wn.Dir || string(got.Node.Kind) != wn.Kind || got.Hash.String() != wn.Hash || len(got.Canonical) != wn.Size {
					t.Errorf("node %d: got dir=%q kind=%s hash=%s size=%d, want dir=%q kind=%s hash=%s size=%d",
						pos, got.Dir, got.Node.Kind, got.Hash, len(got.Canonical), wn.Dir, wn.Kind, wn.Hash, wn.Size)
				}
				if wn.Canonical != nil && string(got.Canonical) != *wn.Canonical {
					t.Errorf("node %d canonical bytes differ:\n got %s\nwant %s", pos, got.Canonical, *wn.Canonical)
				}
			}
			// Build -> Walk restores the (normalized) input.
			assertRoundTrip(t, files, nodes, root)
		})
	}
}

func assertRoundTrip(t *testing.T, files []tree.FileEntry, nodes map[contenthash.Hash][]byte, root contenthash.Hash) {
	t.Helper()
	want := map[string]tree.FileEntry{}
	for _, f := range files {
		p, err := pathx.Normalize(f.Path)
		if err != nil {
			t.Fatal(err)
		}
		f.Path = p
		want[p] = f
	}
	got := map[string]tree.FileEntry{}
	err := tree.Walk(context.Background(), tree.MapGetter(nodes), root, func(p string, e tree.Entry) error {
		if e.Type == tree.TypeFile {
			got[p] = tree.FileEntry{Path: p, Hash: e.Hash, Size: e.Size, Mode: e.Mode}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("walk returned %d files, want %d", len(got), len(want))
	}
	for p, w := range want {
		if got[p] != w {
			t.Fatalf("file %q: got %+v, want %+v", p, got[p], w)
		}
	}
}

func TestDecodeInvalidVectors(t *testing.T) {
	v := loadTreeVectors(t)
	if len(v.DecodeInvalid) == 0 {
		t.Fatal("no decode_invalid vectors")
	}
	for _, c := range v.DecodeInvalid {
		n, err := tree.Decode([]byte(c.Bytes))
		if err == nil {
			t.Errorf("%s: accepted %q as %+v", c.Name, c.Bytes, n)
			continue
		}
		if !errors.Is(err, tree.ErrInvalidNode) && !errors.Is(err, tree.ErrNonCanonical) && !errors.Is(err, tree.ErrUnsupportedFormat) {
			t.Errorf("%s: unexpected error class: %v", c.Name, err)
		}
	}
}

func TestListedNodesDecode(t *testing.T) {
	v := loadTreeVectors(t)
	for _, c := range v.Cases {
		for _, n := range c.Nodes {
			if n.Canonical == nil {
				continue
			}
			b := []byte(*n.Canonical)
			node, err := tree.DecodeVerified(b, contenthash.MustParse(n.Hash))
			if err != nil {
				t.Errorf("%s: listed node does not decode: %v", c.Name, err)
				continue
			}
			if !bytes.Equal(tree.Encode(node), b) {
				t.Errorf("%s: re-encoding differs", c.Name)
			}
		}
	}
}

// TestDecodeValidBuildRejects: the read-time rules accept these nodes (they
// do not depend on Unicode tables), but the write path never produces them.
func TestDecodeValidBuildRejects(t *testing.T) {
	v := loadTreeVectors(t)
	if len(v.DecodeValidBuildRejects) == 0 {
		t.Fatal("no decode_valid_build_rejects vectors")
	}
	for _, c := range v.DecodeValidBuildRejects {
		n, err := tree.Decode([]byte(c.Bytes))
		if err != nil {
			t.Errorf("%s: read-time rules must accept it: %v", c.Name, err)
			continue
		}
		var names []string
		var sibs []pathx.Sibling
		var files []tree.FileEntry
		filesOnly := n.Kind == tree.KindDir
		for _, e := range n.Entries {
			names = append(names, e.Name)
			sibs = append(sibs, pathx.Sibling{Name: e.Name, Dir: e.Type == tree.TypeDir})
			filesOnly = filesOnly && e.Type == tree.TypeFile
			files = append(files, tree.FileEntry{Path: e.Name, Hash: e.Hash, Size: e.Size, Mode: e.Mode})
		}
		for _, p := range n.Pages {
			names = append(names, p.First)
		}
		violated := false
		switch c.WriteRule {
		case "nfc":
			for _, name := range names {
				if norm, err := pathx.NormalizeSegment(name); err == nil && norm != name {
					violated = true
				}
			}
		case "case_conflict":
			violated = pathx.KindOf(pathx.CheckSiblings(sibs)) == pathx.KindCaseConflict
		case "unassigned_char":
			for _, name := range names {
				if _, err := pathx.NormalizeSegment(name); pathx.KindOf(err) == pathx.KindUnassignedChar {
					violated = true
				}
			}
		default:
			t.Fatalf("%s: unknown write_rule %q", c.Name, c.WriteRule)
		}
		if !violated {
			t.Errorf("%s: write rule %s is not violated", c.Name, c.WriteRule)
		}
		if !filesOnly {
			continue
		}
		// For a dir node of files, Build never reproduces these bytes: it
		// rejects the input or normalizes the names.
		root, _, err := tree.Build(files)
		switch c.WriteRule {
		case "nfc":
			if err != nil || root == tree.HashOf([]byte(c.Bytes)) {
				t.Errorf("%s: Build must normalize the name (err %v)", c.Name, err)
			}
		default:
			if string(pathx.KindOf(err)) != c.WriteRule {
				t.Errorf("%s: Build error = %v, want %s", c.Name, err, c.WriteRule)
			}
		}
	}
}
