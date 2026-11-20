package core

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/layout"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/storagekey"
)

// PutObjectRequest is an upload of one whole file.
type PutObjectRequest struct {
	WorkspaceID string
	Body        io.Reader
	// ContentLength is the announced size, or -1 when unknown; a size above
	// the limit is refused before the body is read.
	ContentLength int64
	// Expected is the hash the client announced (X-Content-SHA256), or "".
	Expected string
}

// PutObject streams a file into the workspace: it is hashed while it is
// written to a temporary file next to its final place and then moved
// there once (memory use does not grow with the size), and only after the
// object is durable is it recorded as a staged blob. created is false when
// the workspace already had the content (the blob keeps its status).
func (s *Service) PutObject(ctx context.Context, p ports.Principal, req PutObjectRequest) (blob ports.Blob, created bool, err error) {
	if err := checkID("workspace_id", req.WorkspaceID); err != nil {
		return ports.Blob{}, false, err
	}
	var expected contenthash.Hash
	if req.Expected != "" {
		if expected, err = contenthash.Parse(req.Expected); err != nil {
			return ports.Blob{}, false, invalid("X-Content-SHA256 must be sha256: followed by 64 lowercase hex digits")
		}
	}
	ws, _, err := s.access(ctx, p, req.WorkspaceID, ports.ActionWorkspaceWrite)
	if err != nil {
		return ports.Blob{}, false, err
	}
	if req.ContentLength > s.cfg.MaxUploadBytes {
		return ports.Blob{}, false, s.tooLarge()
	}
	return s.stage(ctx, ws, req.Body, req.ContentLength, expected, nil)
}

// stageFinish finalizes an upload in the same transaction as publication of
// the blob. created distinguishes new content from a verified replay.
type stageFinish func(context.Context, ports.Tx, ports.Blob, bool) error

func (s *Service) stage(ctx context.Context, ws ports.Workspace, body io.Reader, size int64, expected contenthash.Hash, finish stageFinish) (ports.Blob, bool, error) {
	blob := ports.Blob{WorkspaceID: ws.ID, Status: ports.BlobStaged, CreatedAtUs: s.nowUs()}
	var packed layout.Written
	var err error
	if s.cfg.PackSizes.Enabled() && (size < 0 || size >= s.cfg.PackSizes.Chunk) {
		packed, err = layout.Write(ctx, s.d.Objects, s.d.IDs.NewID, ws.StoragePrefix, s.cfg.PackSizes, body, s.cfg.MaxUploadBytes, expected)
		blob.Hash, blob.Size, blob.Chunking = packed.Hash, packed.Size, layout.Fixed
	} else {
		keyOf := func(h contenthash.Hash) string { return storagekey.Join(ws.StoragePrefix, storagekey.ObjectKey(h)) }
		tmpDir := storagekey.Join(ws.StoragePrefix, "objects")
		var info ports.ObjectInfo
		info, _, err = s.d.Objects.PutStream(ctx, tmpDir, body, s.cfg.MaxUploadBytes, expected, keyOf)
		if err == nil {
			blob.Hash, blob.Size, blob.StorageKey = info.Hash, info.Size, storagekey.ObjectKey(info.Hash)
		}
	}
	switch {
	case errors.Is(err, ports.ErrTooLarge):
		return ports.Blob{}, false, s.tooLarge()
	case errors.Is(err, ports.ErrHashMismatch):
		return ports.Blob{}, false, &Error{Code: CodeInvalidRequest, Message: "the content does not match X-Content-SHA256",
			Details: map[string]any{"reason": ReasonChecksumMismatch}, Err: err}
	case err != nil:
		return ports.Blob{}, false, mapErr(err, "object")
	}
	if finish != nil && blob.Size != size {
		return ports.Blob{}, false, invalid("assembled file size differs from the declared size")
	}
	record := func(ctx context.Context, tx ports.Tx, created bool) error {
		if blob.Chunking == layout.Fixed {
			if err := s.d.Store.RecordLayout(ctx, tx, ws.ID, blob.Hash, packed.Packs, packed.File, blob.CreatedAtUs); err != nil {
				return err
			}
		}
		if finish != nil {
			return finish(ctx, tx, blob, created)
		}
		return nil
	}
	var duplicate bool
	err = s.d.Store.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := s.d.Store.InsertBlob(ctx, tx, blob); err != nil {
			duplicate = errors.Is(err, ports.ErrAlreadyExists)
			return err
		}
		return record(ctx, tx, true)
	})
	if !duplicate {
		return blob, err == nil, mapErr(err, "blob")
	}
	old, err := s.d.Store.GetBlob(ctx, nil, ws.ID, blob.Hash)
	if err != nil {
		return ports.Blob{}, false, mapErr(err, "blob")
	}
	if err = s.checkObject(ctx, ws, old); !errors.Is(err, ports.ErrNotFound) {
		if err == nil && finish != nil {
			err = s.d.Store.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error { return finish(ctx, tx, old, false) })
		}
		return old, false, mapErr(err, "object")
	}
	// Repair a missing layout even when the current write settings differ.
	blob.Status, blob.CreatedAtUs = old.Status, old.CreatedAtUs
	err = s.d.Store.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := s.d.Store.SetBlobLayout(ctx, tx, blob); err != nil {
			return err
		}
		return record(ctx, tx, false)
	})
	return blob, false, mapErr(err, "blob")
}

func (s *Service) checkObject(ctx context.Context, ws ports.Workspace, b ports.Blob) error {
	switch b.Chunking {
	case layout.Fixed:
		_, err := layout.Open(ctx, s.d.Store, s.d.Objects, ws, b, 0, -1)
		return err
	case "", "whole":
		i, err := s.d.Objects.Stat(ctx, storageKey(ws, b))
		if err == nil && i.Size != b.Size {
			return ports.ErrNotFound
		}
		return err
	default:
		return ports.ErrStorageUnavailable
	}
}

func (s *Service) tooLarge() *Error {
	return newError(CodeFileTooLarge, fmt.Sprintf("the file is larger than the limit of %d bytes", s.cfg.MaxUploadBytes),
		map[string]any{"max_bytes": s.cfg.MaxUploadBytes})
}
