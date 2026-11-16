package memory

import "github.com/LUOSENGWA/MFGraph/internal/treestore"

// NewTreeStore returns the default tree store (package treestore) over a
// new in-memory BlobStore.
func NewTreeStore(prefixes treestore.PrefixResolver) (*treestore.Store, *BlobStore) {
	blobs := NewBlobStore()
	ts, err := treestore.New(blobs, prefixes)
	if err != nil {
		panic(err) // only fails on invalid static options
	}
	return ts, blobs
}
