package layout

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/storagekey"
)

type span struct {
	chunk  ports.FileChunk
	copies []ports.ChunkLocation
}

// Reader fetches and verifies one chunk at a time, including partial ranges.
// Corrupt or missing replicas are retried from another live pack.
type Reader struct {
	ctx        context.Context
	store      ports.BlobStore
	prefix     string
	start, end int64
	spans      []span
	current    *bytes.Reader
}

// Open also validates coverage and the physical lengths of the requested
// packs before the HTTP caller sends success headers.
func Open(ctx context.Context, db ports.PackRepo, objects ports.BlobStore, ws ports.Workspace, b ports.Blob, offset, length int64) (*Reader, error) {
	if offset < 0 || offset > b.Size {
		return nil, ports.ErrInvalidRange
	}
	end := b.Size
	if length >= 0 && length < b.Size-offset {
		end = offset + length
	}
	r := &Reader{ctx: ctx, store: objects, prefix: ws.StoragePrefix, start: offset, end: end}
	if end == offset {
		return r, nil
	}
	locs, err := db.ChunkLocations(ctx, ws.ID, b.Hash, offset, end)
	if err != nil {
		return nil, err
	}
	sizes := map[string]int64{}
	next := offset
	for _, loc := range locs {
		c := loc.FileChunk
		if c.Length <= 0 || c.Length > MaxChunk || c.Offset < 0 || c.Offset > b.Size-c.Length || loc.PackOffset < 0 ||
			loc.PackOffset > loc.Pack.Size-c.Length || loc.Pack.Key != packKey(loc.Pack.Hash) {
			return nil, ports.ErrNotFound
		}
		n := len(r.spans)
		if n == 0 || r.spans[n-1].chunk != c {
			if min(end, c.Offset+c.Length) <= next || max(offset, c.Offset) != next {
				return nil, ports.ErrNotFound
			}
			r.spans = append(r.spans, span{chunk: c})
			next = min(end, c.Offset+c.Length)
		}
		size, ok := sizes[loc.Pack.Key]
		if !ok {
			stat, err := objects.Stat(ctx, storagekey.Join(ws.StoragePrefix, loc.Pack.Key))
			if err != nil && !errors.Is(err, ports.ErrNotFound) {
				return nil, err
			}
			size = stat.Size
			if err != nil {
				size = -1
			}
			sizes[loc.Pack.Key] = size
		}
		if size == loc.Pack.Size {
			last := &r.spans[len(r.spans)-1]
			last.copies = append(last.copies, loc)
		}
	}
	if next != end {
		return nil, ports.ErrNotFound
	}
	for _, s := range r.spans {
		if len(s.copies) == 0 {
			return nil, ports.ErrNotFound
		}
	}
	return r, nil
}

func (r *Reader) load() error {
	if len(r.spans) == 0 {
		return io.EOF
	}
	s := r.spans[0]
	for _, loc := range s.copies {
		rc, err := r.store.Get(r.ctx, storagekey.Join(r.prefix, loc.Pack.Key), loc.PackOffset, s.chunk.Length)
		if errors.Is(err, ports.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(rc, s.chunk.Length+1))
		err = errors.Join(err, rc.Close())
		if err != nil {
			return err
		}
		if int64(len(b)) != s.chunk.Length || contenthash.SumBytes(b) != s.chunk.Hash {
			continue
		}
		left := max(r.start-s.chunk.Offset, 0)
		right := min(s.chunk.Length, r.end-s.chunk.Offset)
		r.current = bytes.NewReader(b[left:right])
		r.spans = r.spans[1:]
		return nil
	}
	return fmt.Errorf("%w: no intact replica of chunk %s", ports.ErrNotFound, s.chunk.Hash)
}

func (r *Reader) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	if r.current == nil || r.current.Len() == 0 {
		if err := r.load(); err != nil {
			return 0, err
		}
	}
	return r.current.Read(b)
}

func (r *Reader) Close() error { r.current = nil; r.spans = nil; return nil }

// Prime checks the first chunk before a download starts.
func (r *Reader) Prime() error {
	if len(r.spans) == 0 {
		return nil
	}
	return r.load()
}
