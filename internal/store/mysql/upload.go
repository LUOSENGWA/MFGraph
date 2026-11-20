package mysql

import (
	"context"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

func (s *Store) InsertUpload(ctx context.Context, tx ports.Tx, u ports.Upload) error {
	q, err := s.writer(tx)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO upload_sessions
      (upload_id,workspace_id,path,size_bytes,content_type,checksum_sha256,part_size_bytes,total_parts,storage_backend_id,backend_upload_id,status,
       created_by_ref,created_by_principal_uid,created_by_type,created_at_us,expires_at_us) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		u.ID, u.WorkspaceID, []byte(u.Path), u.Size, nullString(u.ContentType), nullString(u.Checksum), u.PartSize, u.Total, u.Backend, u.NativeID, u.Status,
		[]byte(u.Owner), nullString(u.OwnerUID), nullString(u.OwnerType), u.Created, u.Expires)
	return wrap(err)
}

func lockSuffix(tx ports.Tx) string {
	if tx != nil {
		return " FOR UPDATE"
	}
	return ""
}

func (s *Store) GetUpload(ctx context.Context, tx ports.Tx, id string) (u ports.Upload, err error) {
	q, err := s.reader(tx)
	if err != nil {
		return u, err
	}
	err = q.QueryRowContext(ctx, `SELECT upload_id,workspace_id,path,size_bytes,COALESCE(content_type,''),COALESCE(checksum_sha256,''),part_size_bytes,total_parts,
      storage_backend_id,backend_upload_id,status,created_by_ref,COALESCE(created_by_principal_uid,''),COALESCE(created_by_type,''),created_at_us,
      expires_at_us,COALESCE(completed_at_us,0),COALESCE(cleaned_at_us,0),COALESCE(manifest_hash,''),COALESCE(result_hash,'')
      FROM upload_sessions WHERE upload_id=?`+lockSuffix(tx), id).Scan(&u.ID, &u.WorkspaceID, &u.Path, &u.Size, &u.ContentType, &u.Checksum,
		&u.PartSize, &u.Total, &u.Backend, &u.NativeID, &u.Status, &u.Owner, &u.OwnerUID, &u.OwnerType, &u.Created, &u.Expires, &u.Completed, &u.Cleaned, &u.Manifest, &u.Result)
	return u, wrap(err)
}

func (s *Store) UpdateUpload(ctx context.Context, tx ports.Tx, u ports.Upload) error {
	q, err := s.writer(tx)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `UPDATE upload_sessions SET status=?,backend_upload_id=?,manifest_hash=?,result_hash=?,
      completed_at_us=NULLIF(?,0),cleaned_at_us=NULLIF(?,0) WHERE upload_id=?`, u.Status, u.NativeID, nullString(u.Manifest), nullString(u.Result), u.Completed, u.Cleaned, u.ID)
	return wrap(err)
}
func (s *Store) SaveUploadPart(ctx context.Context, tx ports.Tx, id string, p ports.Part, at int64) error {
	q, err := s.writer(tx)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO upload_parts (upload_id,part_number,size_bytes,etag,status,updated_at_us)
        VALUES (?,?,?,?,'uploaded',?) ON DUPLICATE KEY UPDATE size_bytes=VALUES(size_bytes),etag=VALUES(etag),updated_at_us=VALUES(updated_at_us)`, id, p.Number, p.Size, p.ETag, at)
	return wrap(err)
}
func (s *Store) UploadParts(ctx context.Context, tx ports.Tx, id string) ([]ports.Part, error) {
	q, err := s.reader(tx)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, "SELECT part_number,size_bytes,etag FROM upload_parts WHERE upload_id=? AND status='uploaded' ORDER BY part_number"+lockSuffix(tx), id)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()
	var parts []ports.Part
	for rows.Next() {
		var p ports.Part
		if err := rows.Scan(&p.Number, &p.Size, &p.ETag); err != nil {
			return nil, wrap(err)
		}
		parts = append(parts, p)
	}
	return parts, wrap(rows.Err())
}
func (s *Store) PendingUploadCleanup(ctx context.Context, backend string, now int64, after string, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT upload_id FROM upload_sessions WHERE storage_backend_id=? AND upload_id>? AND cleaned_at_us IS NULL
        AND (expires_at_us<=? OR status IN ('completed','aborted','expired')) ORDER BY upload_id LIMIT ?`, backend, after, now, limit)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, wrap(err)
		}
		ids = append(ids, id)
	}
	return ids, wrap(rows.Err())
}
func (s *Store) ClaimBlobUsage(ctx context.Context, tx ports.Tx, ws string, h contenthash.Hash, at int64) (bool, error) {
	q, err := s.writer(tx)
	if err != nil {
		return false, err
	}
	result, err := q.ExecContext(ctx, `UPDATE blobs SET usage_counted_at_us=? WHERE workspace_id=? AND blob_hash=? AND usage_counted_at_us IS NULL`, at, ws, h.Hex())
	if err != nil {
		return false, wrap(err)
	}
	n, err := result.RowsAffected()
	return n == 1, wrap(err)
}

func (s *Store) GetUsage(ctx context.Context, tx ports.Tx, ws string) (u ports.Usage, err error) {
	q, err := s.reader(tx)
	if err != nil {
		return u, err
	}
	err = q.QueryRowContext(ctx, "SELECT IF(usage_started_at_us IS NULL,0,used_bytes),reserved_bytes FROM workspace_usage WHERE workspace_id=?"+lockSuffix(tx), ws).Scan(&u.Used, &u.Reserved)
	return u, wrap(err)
}
func (s *Store) SetUsage(ctx context.Context, tx ports.Tx, ws string, u ports.Usage, at int64) error {
	q, err := s.writer(tx)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `UPDATE workspace_usage SET used_bytes=?,reserved_bytes=?,usage_started_at_us=COALESCE(usage_started_at_us,?),
		version=version+1,updated_at_us=? WHERE workspace_id=?`, u.Used, u.Reserved, at, at, ws)
	return wrap(err)
}
