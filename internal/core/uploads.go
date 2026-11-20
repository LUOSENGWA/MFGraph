package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/pathx"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/storagekey"
)

const (
	defaultPartSize int64 = 16 << 20 // standalone transport, independent of physical chunking
	minPartSize     int64 = 5 << 20
	maxPartSize     int64 = 5 << 30
	maxParts              = 10_000
	maxPartURLs           = 100
	partURLLifetime       = 15 * time.Minute
)

type InitUploadRequest struct {
	WorkspaceID, Path, Checksum, ContentType string
	Size, PartSize                           int64
}
type UploadURL struct {
	Number int
	URL    string
}
type UploadTicket struct {
	ID                string
	PartSize, Expires int64
	Total             int
	Parts             []UploadURL
}
type UploadStatus struct {
	Session  ports.Upload
	Uploaded []ports.Part
	Missing  []int
}
type UploadReceipt struct {
	ID, Path, Hash string
	Size           int64
}

func sessionDir(ws ports.Workspace, id string) string {
	return storagekey.Join(ws.StoragePrefix, "uploads/"+id)
}
func assemblyKey(ws ports.Workspace, id string) string { return sessionDir(ws, id) + "/assembled" }
func sessionPartKey(ws ports.Workspace, id, etag string) string {
	return sessionDir(ws, id) + "/" + etag
}

func uploadExpired() error            { return newError("upload_expired", "the upload session has expired", nil) }
func uploadConflict(msg string) error { return newError(CodeConflict, msg, nil) }

func (s *Service) activeUpload(u ports.Upload) error {
	if u.Status == ports.UploadExpired || (!u.Terminal() && u.Expires <= s.nowUs()) {
		return uploadExpired()
	}
	if u.Status != ports.UploadInitiated && u.Status != ports.Uploading {
		return uploadConflict("the session is no longer accepting parts")
	}
	return nil
}

// The workspace permission and the session owner are checked before revealing
// its progress, returning credentials, or replaying an idempotency receipt.
func (s *Service) uploadAccess(ctx context.Context, p ports.Principal, id string) (ports.Upload, ports.Workspace, error) {
	var ws ports.Workspace
	if s.cfg.HostOwnsProjects {
		return ports.Upload{}, ws, notFound("upload")
	}
	if err := checkID("upload_id", id); err != nil {
		return ports.Upload{}, ws, err
	}
	u, err := s.d.Store.GetUpload(ctx, nil, id)
	if err != nil {
		return u, ws, mapErr(err, "upload")
	}
	if p.Ref != u.Owner || p.ActorType != u.OwnerType {
		return ports.Upload{}, ws, notFound("upload")
	}
	ws, _, err = s.access(ctx, p, u.WorkspaceID, ports.ActionWorkspaceWrite)
	if err != nil {
		return u, ws, err
	}
	if u.Status == ports.UploadExpired || (!u.Terminal() && u.Expires <= s.nowUs()) {
		if err := s.endUpload(ctx, u.ID, ports.UploadExpired); err != nil {
			return u, ws, err
		}
		u, err = s.d.Store.GetUpload(ctx, nil, id)
		if err != nil {
			return u, ws, mapErr(err, "upload")
		}
		if u.Status == ports.UploadExpired {
			return u, ws, uploadExpired()
		}
	}
	return u, ws, nil
}

func partSizeFor(size, hint int64) (int64, int, error) {
	if hint < 0 || hint > maxPartSize {
		return 0, 0, invalid("part_size_bytes must be between 1 byte and 5 GiB when set")
	}
	n := defaultPartSize
	if hint > 0 {
		n = max(minPartSize, min(hint, n))
	}
	if size > n*maxParts {
		n = ((size-1)/(maxParts*minPartSize) + 1) * minPartSize
	}
	return n, int((size + n - 1) / n), nil
}

func (s *Service) InitUpload(ctx context.Context, p ports.Principal, r InitUploadRequest, key string) (UploadTicket, error) {
	var zero UploadTicket
	if s.cfg.HostOwnsProjects {
		return zero, notFound("upload")
	}
	if err := checkID("workspace_id", r.WorkspaceID); err != nil {
		return zero, err
	}
	ws, _, err := s.access(ctx, p, r.WorkspaceID, ports.ActionWorkspaceWrite)
	if err != nil {
		return zero, err
	}
	if r.Path, err = pathx.Normalize(r.Path); err != nil {
		return zero, err
	}
	if r.Size < 0 {
		return zero, invalid("size_bytes must be nonnegative")
	}
	if r.Size > s.cfg.MaxUploadBytes {
		return zero, s.tooLarge()
	}
	partSize, total, err := partSizeFor(r.Size, r.PartSize)
	if err != nil {
		return zero, err
	}
	if err := checkLength("content_type", r.ContentType, 255); err != nil {
		return zero, err
	}
	if r.Checksum != "" {
		h, err := contenthash.Parse("sha256:" + strings.TrimPrefix(r.Checksum, "sha256:"))
		if err != nil {
			return zero, invalid("checksum_sha256 must be a lowercase SHA-256 digest")
		}
		r.Checksum = h.Hex()
	}
	return idempotent(ctx, s, p, "POST /uploads", key, r, func(ctx context.Context, complete completer) (UploadTicket, error) {
		now := s.nowUs()
		u := ports.Upload{ID: s.d.IDs.NewID(), WorkspaceID: ws.ID, Path: r.Path, ContentType: r.ContentType, Size: r.Size, PartSize: partSize, Total: total,
			Checksum: r.Checksum, Backend: ws.StorageBackendID, Owner: p.Ref, OwnerUID: p.PrincipalUID, OwnerType: p.ActorType, Status: ports.UploadOpening, Created: now, Expires: now + s.cfg.UploadTTL.Microseconds()}
		return s.openUpload(ctx, ws, u, complete)
	})
}

func (s *Service) openUpload(ctx context.Context, ws ports.Workspace, u ports.Upload, complete completer) (UploadTicket, error) {
	// Persist the private key and its reservation before S3 can allocate any
	// parts. A crash before the backend responds leaves a reclaimable row.
	err := s.d.Store.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		if err := s.d.Store.InsertUpload(ctx, tx, u); err != nil {
			return err
		}
		return s.adjustReservation(ctx, tx, u, u.Size, 0)
	})
	if err != nil {
		return UploadTicket{}, mapErr(err, "upload")
	}
	var ticket UploadTicket
	err = s.d.Store.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		locked, err := s.d.Store.GetUpload(ctx, tx, u.ID)
		if err != nil {
			return err
		}
		if locked.Status != ports.UploadOpening || locked.Expires <= s.nowUs() {
			return uploadExpired()
		}
		if native, ok := s.d.Objects.(ports.MultipartStore); ok && u.Total > 0 {
			u.NativeID, err = native.StartMultipart(ctx, assemblyKey(ws, u.ID))
			if err != nil && !errors.Is(err, ports.ErrUnsupportedMultipart) {
				return err
			}
		}
		numbers := make([]int, min(16, u.Total))
		for i := range numbers {
			numbers[i] = i + 1
		}
		urls, err := s.makePartURLs(ctx, ws, u, numbers)
		if err != nil {
			return err
		}
		u.Status = ports.UploadInitiated
		if err := s.d.Store.UpdateUpload(ctx, tx, u); err != nil {
			return err
		}
		ticket = UploadTicket{ID: u.ID, PartSize: u.PartSize, Total: u.Total, Expires: u.Expires, Parts: urls}
		return completeIf(ctx, complete, tx, ticket)
	})
	if err != nil {
		// In particular a lost COMMIT acknowledgement must leave an issued
		// ticket intact. Release only an intent that never became active.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		cancelErr := s.d.Store.WithTx(cleanup, func(ctx context.Context, tx ports.Tx) error {
			locked, err := s.d.Store.GetUpload(ctx, tx, u.ID)
			if err != nil || locked.Status != ports.UploadOpening {
				return err
			}
			locked.NativeID = u.NativeID
			return s.releaseUpload(ctx, tx, locked, ports.UploadAborted)
		})
		if cancelErr != nil {
			s.d.Logger.Warn("upload intent cleanup deferred", "upload_id", u.ID, "err", cancelErr)
		} else {
			s.tryCleanup(cleanup, ws, u.ID)
		}
		return UploadTicket{}, mapErr(err, "upload")
	}
	return ticket, nil
}

func (s *Service) adjustReservation(ctx context.Context, tx ports.Tx, u ports.Upload, delta, used int64) error {
	ledger, err := s.d.Store.GetUsage(ctx, tx, u.WorkspaceID)
	if err != nil {
		return err
	}
	if (delta > 0 && ledger.Reserved > math.MaxInt64-delta) || ledger.Reserved+delta < 0 || ledger.Used > math.MaxInt64-used {
		return errors.New("invalid workspace usage balance")
	}
	ledger.Reserved += delta
	ledger.Used += used
	return s.d.Store.SetUsage(ctx, tx, u.WorkspaceID, ledger, s.nowUs())
}

func (s *Service) makePartURLs(ctx context.Context, ws ports.Workspace, u ports.Upload, numbers []int) ([]UploadURL, error) {
	if len(numbers) > maxPartURLs {
		return nil, invalid("request at most 100 part URLs at once")
	}
	seen := make(map[int]bool, len(numbers))
	out := make([]UploadURL, 0, len(numbers))
	for _, n := range numbers {
		if n < 1 || n > u.Total || seen[n] {
			return nil, invalid("part numbers must be distinct and within this session")
		}
		seen[n] = true
		link := UploadURL{Number: n}
		if u.NativeID != "" {
			native, ok := s.d.Objects.(ports.MultipartStore)
			if !ok {
				return nil, mapErr(ports.ErrStorageUnavailable, "multipart")
			}
			ttl := min(partURLLifetime, time.Duration(u.Expires-s.nowUs())*time.Microsecond)
			if ttl < time.Second {
				return nil, uploadExpired()
			}
			url, err := native.PartURL(ctx, assemblyKey(ws, u.ID), u.NativeID, n, ttl)
			if err != nil {
				return nil, mapErr(err, "multipart")
			}
			link.URL = url
		}
		out = append(out, link)
	}
	return out, nil
}

func (s *Service) UploadURLs(ctx context.Context, p ports.Principal, id string, numbers []int, key string) ([]UploadURL, error) {
	u, ws, err := s.uploadAccess(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if err := s.activeUpload(u); err != nil {
		return nil, err
	}
	return idempotent(ctx, s, p, "POST /uploads/{upload_id}/parts", key, struct {
		ID      string
		Numbers []int
	}{id, numbers}, func(ctx context.Context, complete completer) ([]UploadURL, error) {
		urls, err := s.makePartURLs(ctx, ws, u, numbers)
		if err != nil {
			return nil, err
		}
		if complete != nil {
			err = s.d.Store.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error { return complete(ctx, tx, urls) })
		}
		return urls, err
	})
}

// For a live relay session the ledger identifies the accepted ETags and an
// exact directory listing verifies which of those parts still exist.
func (s *Service) currentParts(ctx context.Context, ws ports.Workspace, u ports.Upload) ([]ports.Part, *ports.Part, error) {
	if u.NativeID != "" && !u.Terminal() && u.Status != ports.UploadCompleting {
		native, ok := s.d.Objects.(ports.MultipartStore)
		if !ok {
			return nil, nil, ports.ErrStorageUnavailable
		}
		parts, err := native.ListParts(ctx, assemblyKey(ws, u.ID), u.NativeID)
		return parts, nil, err
	}
	parts, err := s.d.Store.UploadParts(ctx, nil, u.ID)
	if err != nil || u.NativeID != "" || u.Terminal() || len(parts) == 0 {
		return parts, nil, err
	}
	sizes := make(map[string]int64, len(parts))
	for _, p := range parts {
		sizes[sessionPartKey(ws, u.ID, p.ETag)] = -1
	}
	err = s.d.Objects.Walk(ctx, sessionDir(ws, u.ID), func(i ports.ObjectInfo) error {
		if _, ok := sizes[i.Key]; ok {
			sizes[i.Key] = i.Size
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	var missing *ports.Part
	present := slices.DeleteFunc(parts, func(p ports.Part) bool {
		lost := sizes[sessionPartKey(ws, u.ID, p.ETag)] != p.Size
		if lost && missing == nil {
			missing = &p
		}
		return lost
	})
	return present, missing, nil
}

func (s *Service) UploadStatus(ctx context.Context, p ports.Principal, id string) (UploadStatus, error) {
	u, ws, err := s.uploadAccess(ctx, p, id)
	if err != nil {
		return UploadStatus{}, err
	}
	parts, lost, err := s.currentParts(ctx, ws, u)
	if errors.Is(err, ports.ErrNotFound) && u.NativeID != "" {
		// Native completion may have just removed the backend session.
		u, err = s.d.Store.GetUpload(ctx, nil, id)
		if err == nil {
			parts, lost, err = s.currentParts(ctx, ws, u)
		}
	}
	if err == nil && lost != nil && u.Status == ports.UploadCompleting {
		u, err = s.reopenRelay(ctx, ws, u, *lost)
		if err == nil && u.Terminal() {
			parts, err = s.d.Store.UploadParts(ctx, nil, u.ID)
		}
	}
	if err != nil {
		return UploadStatus{}, mapErr(err, "upload parts")
	}
	seen := make(map[int]bool, len(parts))
	parts = slices.DeleteFunc(parts, func(v ports.Part) bool {
		if v.Number < 1 || v.Number > u.Total || v.Size != expectedPartSize(u, v.Number) {
			return true
		}
		seen[v.Number] = true
		return false
	})
	status := UploadStatus{Session: u, Uploaded: parts}
	for n := 1; n <= u.Total; n++ {
		if !seen[n] {
			status.Missing = append(status.Missing, n)
		}
	}
	return status, nil
}

func expectedPartSize(u ports.Upload, n int) int64 {
	if n == u.Total {
		return u.Size - int64(n-1)*u.PartSize
	}
	return u.PartSize
}

func (s *Service) PutPart(ctx context.Context, p ports.Principal, id string, n int, body io.Reader, length int64) (ports.Part, error) {
	var zero ports.Part
	u, ws, err := s.uploadAccess(ctx, p, id)
	if err != nil {
		return zero, err
	}
	if err := s.activeUpload(u); err != nil {
		return zero, err
	}
	if u.NativeID != "" {
		return zero, uploadConflict("use the issued URL for this part")
	}
	if n < 1 || n > u.Total {
		return zero, invalid("part number is outside this session")
	}
	size := expectedPartSize(u, n)
	if length >= 0 && length != size {
		return zero, invalid("part length differs from the negotiated size")
	}
	info, _, err := s.d.Objects.PutStream(ctx, sessionDir(ws, u.ID), body, size, contenthash.Hash{}, func(h contenthash.Hash) string { return sessionPartKey(ws, u.ID, h.Hex()) })
	if errors.Is(err, ports.ErrTooLarge) || (err == nil && info.Size != size) {
		return zero, invalid("part length differs from the negotiated size")
	}
	if err != nil {
		return zero, mapErr(err, "upload part")
	}
	part := ports.Part{Number: n, ETag: info.Hash.Hex(), Size: size}
	err = s.d.Store.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		locked, err := s.d.Store.GetUpload(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := s.activeUpload(locked); err != nil {
			return err
		}
		if err := s.d.Store.SaveUploadPart(ctx, tx, id, part, s.nowUs()); err != nil {
			return err
		}
		if locked.Status == ports.UploadInitiated {
			locked.Status = ports.Uploading
			return s.d.Store.UpdateUpload(ctx, tx, locked)
		}
		return nil
	})
	if err != nil {
		// A terminal cleanup might already have walked the directory while
		// this request was receiving bytes.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if v, e := s.d.Store.GetUpload(cleanup, nil, id); e == nil && v.Terminal() {
			if e := s.deleteSessionBytes(cleanup, ws, id); e != nil {
				// A previously completed sweep may already be acknowledged.
				// Requeue this directory so a transient failure can be retried.
				retryErr := s.d.Store.WithTx(cleanup, func(ctx context.Context, tx ports.Tx) error {
					v, err := s.d.Store.GetUpload(ctx, tx, id)
					if err != nil || v.Cleaned == 0 {
						return err
					}
					v.Cleaned = 0
					return s.d.Store.UpdateUpload(ctx, tx, v)
				})
				s.d.Logger.Warn("late upload part cleanup deferred", "upload_id", id, "err", errors.Join(e, retryErr))
			}
		}
	}
	return part, mapErr(err, "upload part")
}

// Normalize ordered ETags before hashing the completion request. Quoted HTTP
// ETags and their JSON string representation identify the same backend part.
func manifestFor(parts []ports.Part, total int) ([]ports.Part, string, error) {
	if len(parts) != total {
		return nil, "", invalid("completion requires all part numbers")
	}
	parts = slices.Clone(parts)
	for i := range parts {
		p := &parts[i]
		e := p.ETag
		if len(e) >= 2 && e[0] == '"' && e[len(e)-1] == '"' {
			e = e[1 : len(e)-1]
		}
		if p.Number != i+1 || len(e) == 0 || len(e) > 128 || strings.ContainsAny(e, "\r\n\"") {
			return nil, "", invalid("supply ordered consecutive part numbers and their ETags")
		}
		p.ETag = e
		p.Size = 0
	}
	digest, err := requestHash(parts)
	return parts, digest, err
}

func verifyManifest(u ports.Upload, want, stored []ports.Part) error {
	if len(want) != len(stored) {
		return uploadConflict("the parts on storage differ from the completion manifest")
	}
	for i, v := range want {
		got := stored[i]
		if got.Number != v.Number || got.ETag != v.ETag || got.Size != expectedPartSize(u, v.Number) {
			return uploadConflict(fmt.Sprintf("part %d is missing or differs from the completion manifest", v.Number))
		}
	}
	return nil
}
