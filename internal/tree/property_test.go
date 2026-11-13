package tree_test

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/tree"
)

// randomFiles returns a random, conflict-free file set. Names come from a
// small alphabet (with non-ASCII letters) so that directories are shared.
func randomFiles(r *rand.Rand, n int) []tree.FileEntry {
	alphabet := []string{"a", "b", "c", "d", "é", "文", "🚀", "x y", "q\"", "<&>"}
	seen := map[string]bool{}
	var files []tree.FileEntry
	for len(files) < n {
		depth := 1 + r.Intn(4)
		segs := make([]string, depth)
		for i := range segs {
			segs[i] = alphabet[r.Intn(len(alphabet))] + fmt.Sprint(r.Intn(3))
		}
		// Files live in "f*" names, directories in other names, so a path can
		// never be both a file and a directory.
		segs[depth-1] = "f" + segs[depth-1]
		p := strings.Join(segs, "/")
		if seen[p] {
			continue
		}
		seen[p] = true
		mode := tree.ModeRegular
		if r.Intn(5) == 0 {
			mode = tree.ModeExecutable
		}
		content := fmt.Sprintf("%s#%d", p, r.Int())
		files = append(files, tree.FileEntry{Path: p, Hash: contenthash.SumBytes([]byte(content)), Size: int64(len(content)), Mode: mode})
	}
	return files
}

func mustBuild(t *testing.T, files []tree.FileEntry) (contenthash.Hash, map[contenthash.Hash][]byte) {
	t.Helper()
	root, nodes, err := tree.Build(files)
	if err != nil {
		t.Fatal(err)
	}
	return root, nodes
}

func TestPropertyInputOrderDoesNotMatter(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for trial := 0; trial < 50; trial++ {
		files := randomFiles(r, 1+r.Intn(60))
		root, nodes := mustBuild(t, files)
		for k := 0; k < 5; k++ {
			shuffled := append([]tree.FileEntry(nil), files...)
			r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
			root2, nodes2 := mustBuild(t, shuffled)
			if root2 != root || len(nodes2) != len(nodes) {
				t.Fatalf("trial %d: shuffled input gave root %s (%d nodes), want %s (%d nodes)", trial, root2, len(nodes2), root, len(nodes))
			}
		}
	}
}

// dirHashes maps every directory path ("" = root) to its reference hash.
func dirHashes(t *testing.T, root contenthash.Hash, nodes map[contenthash.Hash][]byte) map[string]contenthash.Hash {
	t.Helper()
	m := map[string]contenthash.Hash{"": root}
	err := tree.Walk(context.Background(), tree.MapGetter(nodes), root, func(p string, e tree.Entry) error {
		if e.Type == tree.TypeDir {
			m[p] = e.Hash
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func ancestors(p string) map[string]bool {
	a := map[string]bool{"": true}
	segs := strings.Split(p, "/")
	for i := 1; i < len(segs); i++ {
		a[strings.Join(segs[:i], "/")] = true
	}
	return a
}

func TestPropertyChangeOnlyRewritesPathToRoot(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	for trial := 0; trial < 100; trial++ {
		files := randomFiles(r, 2+r.Intn(80))
		root1, nodes1 := mustBuild(t, files)
		i := r.Intn(len(files))
		changed := append([]tree.FileEntry(nil), files...)
		changed[i].Hash = contenthash.SumBytes([]byte(fmt.Sprintf("changed-%d", trial)))
		root2, nodes2 := mustBuild(t, changed)

		d1, d2 := dirHashes(t, root1, nodes1), dirHashes(t, root2, nodes2)
		anc := ancestors(changed[i].Path)
		for dir, h := range d1 {
			if (d2[dir] != h) != anc[dir] {
				t.Fatalf("trial %d: dir %q changed=%v but ancestor=%v (file %q)", trial, dir, d2[dir] != h, anc[dir], changed[i].Path)
			}
		}
		// Without paging, exactly one new node per directory on the path.
		var fresh int
		for h := range nodes2 {
			if _, ok := nodes1[h]; !ok {
				fresh++
			}
		}
		if fresh != len(anc) {
			t.Fatalf("trial %d: %d new nodes, want %d (dirs on the path to the root)", trial, fresh, len(anc))
		}
	}
}

func TestPropertyChangeInPagedDirectory(t *testing.T) {
	var files []tree.FileEntry
	for i := 0; i < 3000; i++ {
		p := fmt.Sprintf("big/f%05d", i)
		files = append(files, tree.FileEntry{Path: p, Hash: contenthash.SumBytes([]byte(p)), Size: int64(len(p)), Mode: tree.ModeRegular})
	}
	files = append(files, tree.FileEntry{Path: "README.md", Hash: contenthash.SumBytes([]byte("x")), Size: 1, Mode: tree.ModeRegular})
	_, nodes1 := mustBuild(t, files)
	files[1500].Hash = contenthash.SumBytes([]byte("changed"))
	_, nodes2 := mustBuild(t, files)
	var fresh []tree.Kind
	for h, b := range nodes2 {
		if _, ok := nodes1[h]; !ok {
			n, err := tree.Decode(b)
			if err != nil {
				t.Fatal(err)
			}
			fresh = append(fresh, n.Kind)
		}
	}
	// The page holding the file, the directory's index and the root.
	if len(fresh) != 3 {
		t.Fatalf("new nodes = %v, want one dir_page, one dir_index and the root dir", fresh)
	}
}

func TestPropertyNodesRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	for trial := 0; trial < 30; trial++ {
		_, nodes := mustBuild(t, randomFiles(r, 1+r.Intn(50)))
		for h, b := range nodes {
			n, err := tree.DecodeVerified(b, h)
			if err != nil {
				t.Fatalf("decode: %v\n%s", err, b)
			}
			if string(tree.Encode(n)) != string(b) {
				t.Fatalf("re-encoding differs")
			}
		}
	}
}
