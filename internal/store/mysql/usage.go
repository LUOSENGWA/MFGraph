package mysql

import (
	"context"

	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

// BackfillBlobUsage credits a bounded set of old blobs from one workspace.
// Both live uploads and backfills lock blob rows before the workspace ledger.
// Rows inserted later by an older writer are picked up on the next pass.
func (s *Store) BackfillBlobUsage(ctx context.Context, at int64, limit int) (int, error) {
	var count int
	err := s.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		q, err := s.writer(tx)
		if err != nil {
			return err
		}
		rows, err := q.QueryContext(ctx, `SELECT workspace_id,blob_hash,size_bytes FROM blobs
			WHERE usage_counted_at_us IS NULL AND workspace_id=(
				SELECT workspace_id FROM blobs WHERE usage_counted_at_us IS NULL ORDER BY workspace_id,blob_hash LIMIT 1)
			ORDER BY blob_hash LIMIT ? FOR UPDATE`, limit)
		if err != nil {
			return wrap(err)
		}
		defer rows.Close()
		var workspace string
		var hashes []any
		var credit ports.Usage
		for rows.Next() {
			var hash string
			var size int64
			if err := rows.Scan(&workspace, &hash, &size); err != nil {
				return wrap(err)
			}
			hashes = append(hashes, hash)
			credit, err = credit.Add(0, size)
			if err != nil {
				return err
			}
		}
		if err := rows.Err(); err != nil {
			return wrap(err)
		}
		count = len(hashes)
		if count == 0 {
			return nil
		}
		args := append([]any{at, workspace}, hashes...)
		if _, err := q.ExecContext(ctx, `UPDATE blobs SET usage_counted_at_us=? WHERE workspace_id=? AND blob_hash IN (`+placeholders(count)+`)`, args...); err != nil {
			return wrap(err)
		}
		usage, err := s.GetUsage(ctx, tx, workspace)
		if err != nil {
			return err
		}
		usage, err = usage.Add(0, credit.Used)
		if err != nil {
			return err
		}
		return s.SetUsage(ctx, tx, workspace, usage, at)
	})
	return count, err
}
