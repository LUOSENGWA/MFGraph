// Package layout maps whole-file SHA-256 identities to immutable, uncompressed
// packs. Backend keys and chunk boundaries never participate in tree hashes.
package layout

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/scratchfile"
	"github.com/LUOSENGWA/MFGraph/internal/storagekey"
)

const (
	Fixed    = "fixed"
	MaxChunk = 16 << 20
)

type Sizes struct{ Chunk, Pack int64 }

func (s Sizes) Enabled() bool { return s.Chunk != 0 || s.Pack != 0 }

func (s Sizes) Validate() error {
	if s.Enabled() && (s.Chunk < 4<<20 || s.Chunk > MaxChunk || s.Pack < 64<<20 || s.Pack > 256<<20 || s.Pack%s.Chunk != 0) {
		return errors.New("fixed layout requires a 4-16 MiB chunk and a 64-256 MiB pack containing a whole number of chunks")
	}
	return nil
}

type Written struct {
	Hash  contenthash.Hash
	Size  int64
	Packs []ports.PackWrite
	File  []ports.FileChunk
}

// Write keeps one chunk in RAM and at most one pack in temporary disk space.
// The caller registers the blob and the returned layout together after every
// immutable pack has become durable.
func Write(ctx context.Context, objects ports.BlobStore, newID func() string, prefix string, sizes Sizes,
	input io.Reader, limit int64, expected contenthash.Hash) (Written, error) {
	var out Written
	if err := sizes.Validate(); err != nil {
		return out, err
	}
	if !sizes.Enabled() {
		return out, errors.New("packed writing needs explicit sizes")
	}
	if limit < 0 {
		return out, ports.ErrTooLarge
	}
	tmp, clean, err := scratchfile.New("mfgraph-pack-*")
	if err != nil {
		return out, err
	}
	defer clean()
	hash := contenthash.NewHasher()
	limited := &io.LimitedReader{R: input, N: limit}
	src := io.TeeReader(limited, hash)
	buf := make([]byte, sizes.Chunk)
	var chunks []ports.PackChunk
	var used int64
	for {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		n, readErr := io.ReadFull(src, buf)
		if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
			return out, readErr
		}
		if n > 0 {
			sum := contenthash.SumBytes(buf[:n])
			chunks = append(chunks, ports.PackChunk{Hash: sum, Offset: used, Length: int64(n)})
			out.File = append(out.File, ports.FileChunk{Hash: sum, Offset: out.Size, Length: int64(n)})
			if _, err = tmp.Write(buf[:n]); err != nil {
				return out, err
			}
			out.Size += int64(n)
			used += int64(n)
		}
		if used > 0 && (used == sizes.Pack || readErr != nil) {
			pack, err := flush(ctx, objects, prefix, tmp, chunks, newID())
			if err != nil {
				return out, err
			}
			out.Packs = append(out.Packs, pack)
			if err := tmp.Truncate(0); err != nil {
				return out, err
			}
			if _, err := tmp.Seek(0, io.SeekStart); err != nil {
				return out, err
			}
			chunks = nil
			used = 0
		}
		if readErr != nil {
			break
		}
	}
	if limited.N == 0 {
		var extra [1]byte
		n, err := io.ReadFull(input, extra[:])
		if n != 0 {
			return out, ports.ErrTooLarge
		}
		if err != io.EOF {
			return out, err
		}
	}
	out.Hash = hash.Sum()
	if !expected.IsZero() && out.Hash != expected {
		return out, ports.ErrHashMismatch
	}
	return out, nil
}

func packKey(h contenthash.Hash) string { return "packs/" + h.Hex() + ".pack" }

func flush(ctx context.Context, objects ports.BlobStore, prefix string, f *os.File, chunks []ports.PackChunk, id string) (ports.PackWrite, error) {
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].Hash.Hex() < chunks[j].Hash.Hex() })
	var readers []io.Reader
	var out ports.PackWrite
	for _, c := range chunks {
		if n := len(out.Chunks); n > 0 && out.Chunks[n-1].Hash == c.Hash {
			continue
		}
		readers = append(readers, io.NewSectionReader(f, c.Offset, c.Length))
		c.Offset = out.Pack.Size
		out.Pack.Size += c.Length
		out.Chunks = append(out.Chunks, c)
	}
	info, _, err := objects.PutStream(ctx, storagekey.Join(prefix, "packs"), io.MultiReader(readers...), out.Pack.Size,
		contenthash.Hash{}, func(h contenthash.Hash) string { return storagekey.Join(prefix, packKey(h)) })
	if err != nil {
		return out, err
	}
	if info.Size != out.Pack.Size {
		return out, fmt.Errorf("pack length %d, expected %d", info.Size, out.Pack.Size)
	}
	out.Pack.ID, out.Pack.Hash, out.Pack.Key = id, info.Hash, packKey(info.Hash)
	return out, nil
}
