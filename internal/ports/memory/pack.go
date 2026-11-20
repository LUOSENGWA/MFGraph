package memory

import (
	"context"
	"slices"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

func (s *Store) RecordLayout(_ context.Context, tx ports.Tx, ws string, h contenthash.Hash, packs []ports.PackWrite, file []ports.FileChunk, _ int64) error {
	st, err := s.write(tx)
	if err != nil {
		return err
	}
	for _, p := range packs {
		for _, c := range p.Chunks {
			key := blobKey{ws, c.Hash}
			copies := st.packCopies[key]
			if !slices.ContainsFunc(copies, func(l ports.ChunkLocation) bool { return l.Pack.Hash == p.Pack.Hash && l.PackOffset == c.Offset }) {
				st.packCopies[key] = append([]ports.ChunkLocation{{Pack: p.Pack, PackOffset: c.Offset}}, copies...)
			}
		}
	}
	st.layouts[blobKey{ws, h}] = slices.Clone(file)
	return nil
}

func (s *Store) ChunkLocations(_ context.Context, ws string, h contenthash.Hash, start, end int64) ([]ports.ChunkLocation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []ports.ChunkLocation
	for _, part := range s.cur.layouts[blobKey{ws, h}] {
		if part.Offset >= end {
			break
		}
		if part.Offset+part.Length <= start {
			continue
		}
		for _, loc := range s.cur.packCopies[blobKey{ws, part.Hash}] {
			loc.FileChunk = part
			out = append(out, loc)
		}
	}
	return out, nil
}
