package mysql

import "context"

// PruneMetadata removes a bounded batch of expired idempotency records and
// already delivered local outbox rows. A zero publication cutoff preserves all
// delivered rows. Each DELETE rechecks the predicate under the database lock.
func (s *Store) PruneMetadata(ctx context.Context, now, publishedBefore int64, limit int) (int64, int64, error) {
	r, err := s.db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE expires_at_us<=? ORDER BY expires_at_us LIMIT ?`, now, limit)
	if err != nil {
		return 0, 0, wrap(err)
	}
	idem, err := r.RowsAffected()
	if err != nil || publishedBefore == 0 {
		return idem, 0, wrap(err)
	}
	r, err = s.db.ExecContext(ctx, `DELETE FROM outbox_events WHERE status='published' AND published_at_us<=? AND event_type LIKE 'mfgraph.%' ORDER BY published_at_us,event_id LIMIT ?`, publishedBefore, limit)
	if err != nil {
		return idem, 0, wrap(err)
	}
	events, err := r.RowsAffected()
	return idem, events, wrap(err)
}
