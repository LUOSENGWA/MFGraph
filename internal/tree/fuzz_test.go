package tree_test

import (
	"bytes"
	"io/fs"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/tree"
)

func fsMode(perm uint32) fs.FileMode { return fs.FileMode(perm) }

// FuzzDecode checks that Decode never panics and that every accepted input
// is exactly the canonical encoding of what it decodes to.
func FuzzDecode(f *testing.F) {
	v := loadTreeVectors(f)
	for _, c := range v.Cases {
		for _, n := range c.Nodes {
			if n.Canonical != nil {
				f.Add([]byte(*n.Canonical))
			}
		}
	}
	for _, c := range v.DecodeInvalid {
		if c.BytesHex != "" {
			f.Add(mustHex(f, c.BytesHex))
		}
	}
	for _, c := range v.DecodeValidBuildRejects {
		f.Add(mustHex(f, c.BytesHex))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		n, err := tree.Decode(b)
		if err != nil {
			return
		}
		if !bytes.Equal(tree.Encode(n), b) {
			t.Fatalf("accepted non-canonical input %q", b)
		}
		if err := n.Validate(); err != nil {
			t.Fatalf("accepted invalid node: %v", err)
		}
	})
}
