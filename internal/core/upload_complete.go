package core

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

func receipt(u ports.Upload) UploadReceipt {
	return UploadReceipt{ID: u.ID, Path: u.Path, Size: u.Size, Hash: "sha256:" + u.Result}
}

// freezeUpload records exactly what may be assembled before touching a native
// multipart session. After a crash the same manifest can continue from either
// the native upload or the completed private object.
func (s *Service) freezeUpload(ctx context.Context, ws ports.Workspace, u ports.Upload, want []ports.Part, digest string) (ports.Upload, error) {
	var remote []ports.Part
	var remoteErr error
	if u.NativeID != "" && !u.Terminal() && u.Status != ports.UploadCompleting {
		remote, remoteErr = s.currentParts(ctx, ws, u)
	}
	err := s.d.Store.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		u, err = s.d.Store.GetUpload(ctx, tx, u.ID)
		if err != nil {
			return err
		}
		if !u.Terminal() && u.Expires <= s.nowUs() || u.Status == ports.UploadExpired {
			return uploadExpired()
		}
		if u.Status == ports.UploadCompleting || u.Status == ports.UploadCompleted {
			if u.Manifest != digest {
				return uploadConflict("a different completion manifest was already accepted")
			}
			return nil
		}
		if err := s.activeUpload(u); err != nil {
			return err
		}
		stored := remote
		if u.NativeID == "" {
			stored, err = s.d.Store.UploadParts(ctx, tx, u.ID)
		} else {
			err = remoteErr
		}
		if err != nil {
			return err
		}
		if err := verifyManifest(u, want, stored); err != nil {
			return err
		}
		if u.NativeID != "" {
			for _, part := range stored {
				if err := s.d.Store.SaveUploadPart(ctx, tx, u.ID, part, s.nowUs()); err != nil {
					return err
				}
			}
		}
		u.Status, u.Manifest = ports.UploadCompleting, digest
		return s.d.Store.UpdateUpload(ctx, tx, u)
	})
	return u, mapErr(err, "upload")
}

func (s *Service) assembly(ctx context.Context, ws ports.Workspace, u ports.Upload, parts []ports.Part) (io.ReadCloser, error) {
	if u.NativeID == "" {
		// Only one part is open at a time. Closing the reader on cancellation
		// or a storage error unblocks the pipe writer.
		reader, writer := io.Pipe()
		go func() { writer.CloseWithError(s.copyParts(ctx, ws, u, parts, writer)) }()
		return reader, nil
	}
	key := assemblyKey(ws, u.ID)
	native, ok := s.d.Objects.(ports.MultipartStore)
	if !ok {
		return nil, ports.ErrStorageUnavailable
	}
	info, err := s.d.Objects.Stat(ctx, key)
	if errors.Is(err, ports.ErrNotFound) {
		err = native.FinishMultipart(ctx, key, u.NativeID, parts)
		if err != nil && !errors.Is(err, ports.ErrNotFound) {
			return nil, err
		}
		info, err = s.d.Objects.Stat(ctx, key)
	}
	if err != nil {
		return nil, err
	}
	if info.Size != u.Size {
		return nil, uploadConflict("assembled object has an unexpected size")
	}
	return s.d.Objects.Get(ctx, key, 0, -1)
}

func (s *Service) copyParts(ctx context.Context, ws ports.Workspace, u ports.Upload, parts []ports.Part, out io.Writer) error {
	for _, part := range parts {
		r, err := s.d.Objects.Get(ctx, sessionPartKey(ws, u.ID, part.ETag), 0, -1)
		if err != nil {
			return err
		}
		h := contenthash.NewHasher()
		n, copyErr := io.Copy(io.MultiWriter(out, h), r)
		if err := errors.Join(copyErr, r.Close()); err != nil {
			return err
		}
		if n != expectedPartSize(u, part.Number) || h.Sum().Hex() != part.ETag {
			return invalidReason(ReasonChecksumMismatch, "a stored part does not match its ETag or size")
		}
	}
	return nil
}

func (s *Service) CompleteUpload(ctx context.Context, p ports.Principal, id string, parts []ports.Part, key string) (UploadReceipt, error) {
	var zero UploadReceipt
	u, ws, err := s.uploadAccess(ctx, p, id)
	if err != nil {
		return zero, err
	}
	want, digest, err := manifestFor(parts, u.Total)
	if err != nil {
		return zero, err
	}
	return idempotent(ctx, s, p, "POST /uploads/{upload_id}/complete", key, struct{ ID, Manifest string }{id, digest}, func(ctx context.Context, complete completer) (UploadReceipt, error) {
		frozen, err := s.freezeUpload(ctx, ws, u, want, digest)
		if err != nil {
			return zero, err
		}
		var result UploadReceipt
		// Replays, including after an ambiguous commit acknowledgement, do
		// not need any temporary bytes.
		repeat := func(ctx context.Context, u ports.Upload) (UploadReceipt, error) {
			v := receipt(u)
			if complete == nil {
				return v, nil
			}
			return v, s.d.Store.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error { return complete(ctx, tx, v) })
		}
		if frozen.Status == ports.UploadCompleted {
			return repeat(ctx, frozen)
		}
		body, err := s.assembly(ctx, ws, frozen, want)
		if err == nil {
			defer body.Close()
			var expected contenthash.Hash
			if frozen.Checksum != "" {
				expected, err = contenthash.ParseHex(contenthash.AlgoSHA256, frozen.Checksum)
				if err != nil {
					return zero, err
				}
			}
			_, _, err = s.stage(ctx, ws, body, frozen.Size, expected, func(ctx context.Context, tx ports.Tx, b ports.Blob, created bool) error {
				v, err := s.d.Store.GetUpload(ctx, tx, id)
				if err != nil {
					return err
				}
				if v.Manifest != digest {
					return uploadConflict("completion manifest changed")
				}
				switch v.Status {
				case ports.UploadCompleted:
				case ports.UploadCompleting:
					if v.Expires <= s.nowUs() {
						return uploadExpired()
					}
					var used int64
					if created {
						used = b.Size
					}
					if err := s.adjustReservation(ctx, tx, v, -v.Size, used); err != nil {
						return err
					}
					v.Status, v.Completed, v.Result = ports.UploadCompleted, s.nowUs(), b.Hash.Hex()
					if err := s.d.Store.UpdateUpload(ctx, tx, v); err != nil {
						return err
					}
				case ports.UploadExpired:
					return uploadExpired()
				default:
					return uploadConflict("upload was aborted during assembly")
				}
				result = receipt(v)
				return completeIf(ctx, complete, tx, result)
			})
		}
		if err != nil {
			// A simultaneous completion may already have cleaned the parts.
			if v, readErr := s.d.Store.GetUpload(ctx, nil, id); readErr == nil && v.Status == ports.UploadCompleted && v.Manifest == digest {
				return repeat(ctx, v)
			}
			return zero, mapErr(err, "upload assembly")
		}
		s.tryCleanup(ctx, ws, id)
		return result, nil
	})
}

// endUpload releases the reservation exactly once. Expiry racing with a
// successful completion or another janitor observes the terminal state.
func (s *Service) endUpload(ctx context.Context, id, status string) error {
	return s.d.Store.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		u, err := s.d.Store.GetUpload(ctx, tx, id)
		if err != nil {
			return err
		}
		if u.Terminal() {
			if status == ports.UploadAborted && u.Status == ports.UploadCompleted {
				return uploadConflict("the upload was already completed")
			}
			return nil
		}
		if status == ports.UploadExpired && u.Expires > s.nowUs() {
			return nil
		}
		return s.releaseUpload(ctx, tx, u, status)
	})
}

func (s *Service) releaseUpload(ctx context.Context, tx ports.Tx, u ports.Upload, status string) error {
	if err := s.adjustReservation(ctx, tx, u, -u.Size, 0); err != nil {
		return err
	}
	u.Status = status
	return s.d.Store.UpdateUpload(ctx, tx, u)
}

func (s *Service) AbortUpload(ctx context.Context, p ports.Principal, id string) error {
	_, ws, err := s.uploadAccess(ctx, p, id)
	if err != nil {
		return err
	}
	if err := s.endUpload(ctx, id, ports.UploadAborted); err != nil {
		return err
	}
	return s.cleanupUpload(ctx, ws, id)
}

func (s *Service) cleanupUpload(ctx context.Context, ws ports.Workspace, id string) error {
	u, err := s.d.Store.GetUpload(ctx, nil, id)
	if err != nil {
		return err
	}
	if !u.Terminal() || u.Cleaned != 0 {
		return nil
	}
	if u.NativeID != "" {
		native, ok := s.d.Objects.(ports.MultipartStore)
		if !ok {
			return ports.ErrStorageUnavailable
		}
		key := assemblyKey(ws, id)
		if err := native.AbortMultipart(ctx, key, u.NativeID); err != nil {
			return err
		}
		i, err := s.d.Objects.Stat(ctx, key)
		if err != nil && !errors.Is(err, ports.ErrNotFound) {
			return err
		}
		if err == nil {
			if store, ok := s.d.Objects.(interface {
				DeleteVersion(context.Context, string, string) error
			}); ok {
				err = store.DeleteVersion(ctx, key, i.VersionID)
			} else {
				err = s.d.Objects.Delete(ctx, key)
			}
			if err != nil {
				return err
			}
		}
	} else {
		if native, ok := s.d.Objects.(ports.MultipartStore); ok && u.Status != ports.UploadCompleted && u.Total > 0 {
			if err := native.AbortMultipart(ctx, assemblyKey(ws, id), ""); err != nil {
				return err
			}
		}
		if err := s.d.Objects.Walk(ctx, sessionDir(ws, id), func(i ports.ObjectInfo) error {
			if store, ok := s.d.Objects.(interface {
				DeleteVersion(context.Context, string, string) error
			}); ok && i.VersionID != "" {
				return store.DeleteVersion(ctx, i.Key, i.VersionID)
			}
			return s.d.Objects.Delete(ctx, i.Key)
		}); err != nil {
			return err
		}
	}
	return s.d.Store.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		u, err := s.d.Store.GetUpload(ctx, tx, id)
		if err != nil {
			return err
		}
		if u.Cleaned != 0 {
			return nil
		}
		u.Cleaned = s.nowUs()
		return s.d.Store.UpdateUpload(ctx, tx, u)
	})
}

func (s *Service) tryCleanup(ctx context.Context, ws ports.Workspace, id string) {
	cleanup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.cleanupUpload(cleanup, ws, id); err != nil {
		s.d.Logger.Warn("upload cleanup deferred", "upload_id", id, "err", err)
	}
}

// ReapUploads retries unfinished private cleanup and releases expired
// reservations, including sessions whose clients never returned.
func (s *Service) ReapUploads(ctx context.Context) error {
	if s.cfg.HostOwnsProjects {
		return nil
	}
	var after string
	for {
		ids, err := s.d.Store.PendingUploadCleanup(ctx, s.cfg.StorageBackendID, s.nowUs(), after, 32)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := s.endUpload(ctx, id, ports.UploadExpired); err != nil {
				return err
			}
			u, err := s.d.Store.GetUpload(ctx, nil, id)
			if err != nil {
				return err
			}
			ws, err := s.d.Store.GetWorkspace(ctx, nil, u.WorkspaceID)
			if err != nil {
				return err
			}
			if err := s.cleanupUpload(ctx, ws, id); err != nil {
				s.d.Logger.Warn("upload cleanup deferred", "upload_id", id, "err", err)
			}
		}
		if len(ids) < 32 {
			return nil
		}
		after = ids[len(ids)-1]
	}
}
