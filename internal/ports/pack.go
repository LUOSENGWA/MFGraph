package ports

import (
	"context"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
)

// Pack is an immutable physical object. Key is relative to one workspace.
type Pack struct {
	ID, Key string
	Hash    contenthash.Hash
	Size    int64
}

type PackChunk struct {
	Hash           contenthash.Hash
	Offset, Length int64
}

type PackWrite struct {
	Pack   Pack
	Chunks []PackChunk // offsets within the pack
}

// FileChunk places a chunk in the logical file, independent of its replicas.
type FileChunk struct {
	Hash           contenthash.Hash
	Offset, Length int64
}

type ChunkLocation struct {
	FileChunk
	Pack       Pack
	PackOffset int64
}

type PackRepo interface {
	// RecordSmallPack converts still-staged whole blobs to single-chunk layouts in the commit transaction.
	RecordSmallPack(ctx context.Context, tx Tx, workspace string, pack PackWrite, at int64) error
	SetBlobLayout(ctx context.Context, tx Tx, b Blob) error
	// RecordLayout shares the transaction that first registers the blob.
	RecordLayout(context.Context, Tx, string, contenthash.Hash, []PackWrite, []FileChunk, int64) error
	// ChunkLocations returns all replicas overlapping [start,end), ordered
	// by file offset and then newest pack first.
	ChunkLocations(ctx context.Context, workspace string, hash contenthash.Hash, start, end int64) ([]ChunkLocation, error)
}
