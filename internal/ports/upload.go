package ports

import (
	"context"
	"errors"
	"math"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
)

// Upload holds the authority, sizes and durable state of a resumable session.
// Manifest is the digest of the ordered part numbers and ETags accepted for
// completion. Result is the verified whole-file SHA-256 in database form.
type Upload struct {
	ID, WorkspaceID, Path, ContentType, Checksum          string
	Size, PartSize                                        int64
	Total                                                 int
	Backend, NativeID, Status, Owner, OwnerUID, OwnerType string
	Created, Expires, Completed, Cleaned                  int64
	Manifest, Result                                      string
}

const (
	UploadOpening    = "opening"
	UploadInitiated  = "initiated"
	Uploading        = "uploading"
	UploadCompleting = "completing"
	UploadCompleted  = "completed"
	UploadAborted    = "aborted"
	UploadExpired    = "expired"
)

func (u Upload) Terminal() bool {
	return u.Status == UploadCompleted || u.Status == UploadAborted || u.Status == UploadExpired
}

type Usage struct{ Used, Reserved int64 }

func (u Usage) Add(reserved, used int64) (Usage, error) {
	if used < 0 || (reserved > 0 && u.Reserved > math.MaxInt64-reserved) || u.Reserved+reserved < 0 || u.Used > math.MaxInt64-used {
		return u, errors.New("invalid workspace usage balance")
	}
	return Usage{Used: u.Used + used, Reserved: u.Reserved + reserved}, nil
}

type UploadRepo interface {
	InsertUpload(context.Context, Tx, Upload) error
	// GetUpload takes a write lock when tx is supplied. Writers lock the
	// session first, then change its parts and reservation in the same tx.
	GetUpload(ctx context.Context, tx Tx, id string) (Upload, error)
	UpdateUpload(context.Context, Tx, Upload) error
	SaveUploadPart(ctx context.Context, tx Tx, id string, part Part, at int64) error
	UploadParts(ctx context.Context, tx Tx, id string) ([]Part, error)
	// PendingUploadCleanup returns expired active sessions and terminal
	// sessions whose private storage has yet to be cleaned.
	PendingUploadCleanup(ctx context.Context, backend string, now int64, after string, limit int) ([]string, error)
	// ClaimBlobUsage marks a workspace blob once. The caller credits its
	// verified size in the same transaction when claimed is true.
	ClaimBlobUsage(ctx context.Context, tx Tx, workspace string, hash contenthash.Hash, at int64) (claimed bool, err error)
	// GetUsage locks the ledger when tx is supplied.
	GetUsage(ctx context.Context, tx Tx, workspace string) (Usage, error)
	SetUsage(ctx context.Context, tx Tx, workspace string, usage Usage, at int64) error
}
