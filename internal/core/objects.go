package core

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
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
	keyOf := func(h contenthash.Hash) string { return storagekey.Join(ws.StoragePrefix, storagekey.ObjectKey(h)) }
	tmpDir := storagekey.Join(ws.StoragePrefix, "objects")
	info, _, err := s.d.Objects.PutStream(ctx, tmpDir, req.Body, s.cfg.MaxUploadBytes, expected, keyOf)
	switch {
	case errors.Is(err, ports.ErrTooLarge):
		return ports.Blob{}, false, s.tooLarge()
	case errors.Is(err, ports.ErrHashMismatch):
		return ports.Blob{}, false, &Error{Code: CodeInvalidRequest, Message: "the content does not match X-Content-SHA256",
			Details: map[string]any{"reason": ReasonChecksumMismatch}, Err: err}
	case err != nil:
		return ports.Blob{}, false, mapErr(err, "object")
	}
	blob = ports.Blob{WorkspaceID: ws.ID, Hash: info.Hash, Size: info.Size, StorageKey: storagekey.ObjectKey(info.Hash),
		Status: ports.BlobStaged, CreatedAtUs: s.nowUs()}
	err = s.d.Store.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error { return s.d.Store.InsertBlob(ctx, tx, blob) })
	if errors.Is(err, ports.ErrAlreadyExists) {
		old, err := s.d.Store.GetBlob(ctx, nil, ws.ID, info.Hash)
		return old, false, mapErr(err, "blob")
	}
	if err != nil {
		return ports.Blob{}, false, mapErr(err, "blob")
	}
	return blob, true, nil
}

func (s *Service) tooLarge() *Error {
	return newError(CodeFileTooLarge, fmt.Sprintf("the file is larger than the limit of %d bytes", s.cfg.MaxUploadBytes),
		map[string]any{"max_bytes": s.cfg.MaxUploadBytes})
}
