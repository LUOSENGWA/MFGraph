package mysql

import (
	"context"
	"strings"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

func (s *Store) RecordLayout(ctx context.Context, tx ports.Tx, ws string, hash contenthash.Hash, packs []ports.PackWrite, file []ports.FileChunk, at int64) error {
	q, err := s.writer(tx)
	if err != nil {
		return err
	}
	for _, pack := range packs {
		if err := recordPack(ctx, q, ws, pack, at); err != nil {
			return err
		}
	}
	for len(file) > 0 {
		n := min(batch, len(file))
		args := make([]any, 0, n*5)
		for _, c := range file[:n] {
			args = append(args, ws, hash.Hex(), c.Offset, c.Hash.Hex(), c.Length)
		}
		_, err := q.ExecContext(ctx, `INSERT INTO blob_chunks (workspace_id,blob_hash,file_offset,chunk_hash,length_bytes) VALUES `+
			strings.TrimSuffix(strings.Repeat("(?,?,?,?,?),", n), ","), args...)
		if err != nil {
			return wrap(err)
		}
		file = file[n:]
	}
	return nil
}

func (s *Store) ChunkLocations(ctx context.Context, ws string, h contenthash.Hash, start, end int64) ([]ports.ChunkLocation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT b.file_offset,b.length_bytes,b.chunk_hash,c.offset_bytes,p.pack_id,p.storage_key,p.size_bytes,p.sha256
		FROM blob_chunks b JOIN chunks c ON c.workspace_id=b.workspace_id AND c.chunk_hash=b.chunk_hash AND c.length_bytes=b.length_bytes
		JOIN packs p ON p.workspace_id=c.workspace_id AND p.pack_id=c.pack_id
		WHERE b.workspace_id=? AND b.blob_hash=? AND b.file_offset<? AND b.file_offset+b.length_bytes>? AND p.status='live'
		ORDER BY b.file_offset,p.created_at_us DESC,p.pack_id,c.offset_bytes`, ws, h.Hex(), end, start)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()
	var out []ports.ChunkLocation
	for rows.Next() {
		var loc ports.ChunkLocation
		var chunk, pack string
		if err := rows.Scan(&loc.Offset, &loc.Length, &chunk, &loc.PackOffset, &loc.Pack.ID, &loc.Pack.Key, &loc.Pack.Size, &pack); err != nil {
			return nil, wrap(err)
		}
		if loc.Hash, err = parseHash(contenthash.AlgoSHA256, chunk); err != nil {
			return nil, err
		}
		if loc.Pack.Hash, err = parseHash(contenthash.AlgoSHA256, pack); err != nil {
			return nil, err
		}
		out = append(out, loc)
	}
	return out, wrap(rows.Err())
}

// SetBlobLayout locks the existing blob and removes any old logical offsets.
// The caller inserts the replacement offsets in this transaction. Live status
// and the original creation time are preserved.
func (s *Store) SetBlobLayout(ctx context.Context, tx ports.Tx, b ports.Blob) error {
	q, err := s.writer(tx)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, "UPDATE blobs SET storage_key=?, chunking=? WHERE workspace_id=? AND blob_hash=?", []byte(b.StorageKey), nullString(b.Chunking), b.WorkspaceID, b.Hash.Hex())
	if err != nil {
		return wrap(err)
	}
	_, err = q.ExecContext(ctx, "DELETE FROM blob_chunks WHERE workspace_id=? AND blob_hash=?", b.WorkspaceID, b.Hash.Hex())
	return wrap(err)
}

func recordPack(ctx context.Context, q querier, ws string, w ports.PackWrite, at int64) error {
	p := w.Pack
	_, err := q.ExecContext(ctx, `INSERT INTO packs (workspace_id,pack_id,storage_key,size_bytes,sha256,created_at_us)
			VALUES (?,?,?,?,?,?) ON DUPLICATE KEY UPDATE sha256=sha256`, ws, p.ID, []byte(p.Key), p.Size, p.Hash.Hex(), at)
	if err != nil {
		return wrap(err)
	}
	var id string
	if err = q.QueryRowContext(ctx, "SELECT pack_id FROM packs WHERE workspace_id=? AND sha256=? FOR UPDATE", ws, p.Hash.Hex()).Scan(&id); err != nil {
		return wrap(err)
	}
	for len(w.Chunks) > 0 {
		n := min(len(w.Chunks), batch)
		args := make([]any, 0, n*6)
		for _, c := range w.Chunks[:n] {
			args = append(args, ws, c.Hash.Hex(), id, c.Offset, c.Length, at)
		}
		_, err := q.ExecContext(ctx, `INSERT INTO chunks (workspace_id,chunk_hash,pack_id,offset_bytes,length_bytes,created_at_us) VALUES `+
			strings.TrimSuffix(strings.Repeat("(?,?,?,?,?,?),", n), ",")+" ON DUPLICATE KEY UPDATE length_bytes=length_bytes", args...)
		if err != nil {
			return wrap(err)
		}
		w.Chunks = w.Chunks[n:]
	}
	return nil
}

// RecordSmallPack locks staged blob rows in hash order. A concurrent commit or
// re-upload can finish first; its published layout is kept.
func (s *Store) RecordSmallPack(ctx context.Context, tx ports.Tx, ws string, pack ports.PackWrite, at int64) error {
	q, err := s.writer(tx)
	if err != nil {
		return err
	}
	if err := recordPack(ctx, q, ws, pack, at); err != nil {
		return err
	}
	chunks := pack.Chunks
	for len(chunks) > 0 {
		n := min(len(chunks), batch)
		hashes := make([]contenthash.Hash, 0, n)
		for _, c := range chunks[:n] {
			hashes = append(hashes, c.Hash)
		}
		blobs, err := s.GetBlobs(ctx, tx, ws, hashes)
		if err != nil {
			return err
		}
		args := []any{ws}
		var rows []any
		for _, h := range hashes {
			b, ok := blobs[h]
			if ok && b.Status == ports.BlobStaged && (b.Chunking == "" || b.Chunking == "whole") {
				args = append(args, h.Hex())
				rows = append(rows, ws, h.Hex(), 0, h.Hex(), b.Size)
			}
		}
		if len(rows) > 0 {
			_, err = q.ExecContext(ctx, `INSERT INTO blob_chunks (workspace_id,blob_hash,file_offset,chunk_hash,length_bytes) VALUES `+
				strings.TrimSuffix(strings.Repeat("(?,?,?,?,?),", len(args)-1), ","), rows...)
			if err != nil {
				return wrap(err)
			}
			_, err = q.ExecContext(ctx, "UPDATE blobs SET chunking='fixed',storage_key='' WHERE workspace_id=? AND blob_hash IN ("+placeholders(len(args)-1)+")", args...)
			if err != nil {
				return wrap(err)
			}
		}
		chunks = chunks[n:]
	}
	return nil
}
