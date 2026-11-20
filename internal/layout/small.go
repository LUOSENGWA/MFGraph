package layout

import (
	"context"
	"errors"
	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/storagekey"
	"io"
	"sort"
)

// SmallPacks groups newly referenced whole files at commit time. A durable
// staging object makes each upload independently usable before the commit.
// Each small file then has a single logical chunk with its original hash.
func SmallPacks(ctx context.Context, objects ports.BlobStore, newID func() string, prefix string, sizes Sizes, blobs map[contenthash.Hash]ports.Blob) ([]ports.PackWrite, error) {
	if !sizes.Enabled() {
		return nil, nil
	}
	var files []ports.Blob
	for _, b := range blobs {
		if b.Status == ports.BlobStaged && (b.Chunking == "" || b.Chunking == "whole") && b.Size > 0 && b.Size < sizes.Chunk {
			files = append(files, b)
		}
	}
	if len(files) < 2 {
		return nil, nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Hash.Hex() < files[j].Hash.Hex() })
	tmp, clean, err := scratchPack()
	if err != nil {
		return nil, err
	}
	defer clean()
	var packs []ports.PackWrite
	for len(files) > 0 {
		var chunks []ports.PackChunk
		var used int64
		for len(files) > 0 && used+files[0].Size <= sizes.Pack {
			b := files[0]
			reader, err := objects.Get(ctx, storagekey.Join(prefix, b.StorageKey), 0, -1)
			if err != nil {
				return nil, err
			}
			h := contenthash.NewHasher()
			n, over, readErr := ports.ReadLimited(reader, io.MultiWriter(tmp, h), b.Size)
			if err := errors.Join(readErr, reader.Close()); err != nil {
				return nil, err
			}
			if over || n != b.Size || h.Sum() != b.Hash {
				return nil, ports.ErrHashMismatch
			}
			chunks = append(chunks, ports.PackChunk{Hash: b.Hash, Length: n, Offset: used})
			used += n
			files = files[1:]
		}
		pack, err := flush(ctx, objects, prefix, tmp, chunks, newID())
		if err != nil {
			return nil, err
		}
		packs = append(packs, pack)
		if err := tmp.Truncate(0); err != nil {
			return nil, err
		}
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
	}
	return packs, nil
}
