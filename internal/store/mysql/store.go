// Package mysql implements ports.Store on a MySQL-protocol database: MySQL
// 8.0 and seekdb (a subset of MySQL 5.7). It uses only what both provide:
// no CTEs, window functions, JSON functions, SKIP LOCKED / NOWAIT,
// RETURNING or generated columns; times are BIGINT epoch microseconds from
// the Clock; names, paths, refs and actor references are VARBINARY and are
// read as bytes, never through a character set conversion; pages are
// selected with keyset conditions, never with OFFSET.
package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

// Store is the MySQL ports.Store.
type Store struct {
	db *sql.DB
}

var _ ports.Store = (*Store)(nil)

// New returns a store on db (a go-sql-driver/mysql connection pool).
func New(db *sql.DB) *Store { return &Store{db: db} }

// Ping checks that the database answers (readiness).
func (s *Store) Ping(ctx context.Context) error { return wrap(s.db.PingContext(ctx)) }

type txn struct {
	store    *Store
	tx       *sql.Tx
	done     bool
	eventIDs []any
}

func (*txn) TxKind() string { return "mysql" }

// ErrForeignTx: a Tx of another Transactor, or a finished one, was used.
var ErrForeignTx = errors.New("mysql: transaction does not belong to this store or is finished")

// WithTx implements ports.Transactor.
func (s *Store) WithTx(ctx context.Context, fn func(ctx context.Context, tx ports.Tx) error) error {
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrap(err)
	}
	t := &txn{store: s, tx: sqlTx}
	committed := false
	defer func() {
		t.done = true
		if !committed {
			_ = sqlTx.Rollback()
		}
	}()
	if err := fn(ctx, t); err != nil {
		return err
	}
	if len(t.eventIDs) > 0 {
		if err := t.orderEvents(ctx); err != nil {
			return err
		}
	}
	if err := sqlTx.Commit(); err != nil {
		return wrap(err)
	}
	committed = true
	return nil
}

// Only the last two statements before COMMIT take the shared sequence lock.
// No transaction can commit a later batch until the previous one commits or
// rolls back. An auto-increment ID assigned on INSERT cannot provide this.
func (t *txn) orderEvents(ctx context.Context) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO outbox_sequence (singleton, last_batch) VALUES (1, 1)
		ON DUPLICATE KEY UPDATE last_batch = last_batch + 1`)
	if err != nil {
		return wrap(err)
	}
	_, err = t.tx.ExecContext(ctx, `UPDATE outbox_events SET commit_seq =
		(SELECT last_batch FROM outbox_sequence WHERE singleton = 1) WHERE event_id IN (`+
		placeholders(len(t.eventIDs))+`)`, t.eventIDs...)
	return wrap(err)
}

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// reader returns the transaction of tx, or the pool for a nil tx.
func (s *Store) reader(tx ports.Tx) (querier, error) {
	if tx == nil {
		return s.db, nil
	}
	return s.writer(tx)
}

// writer returns the transaction of tx; writes need one.
func (s *Store) writer(tx ports.Tx) (querier, error) {
	t, ok := tx.(*txn)
	if !ok || t.store != s || t.done {
		return nil, ErrForeignTx
	}
	return t.tx, nil
}

const (
	errDupEntry        = 1062 // ER_DUP_ENTRY
	errLockWaitTimeout = 1205 // ER_LOCK_WAIT_TIMEOUT (MySQL; seekdb also for deadlocks)
	errDeadlock        = 1213 // ER_LOCK_DEADLOCK (MySQL)
)

// wrap maps driver errors to the errors of package ports: no rows to
// ErrNotFound, a duplicate key to ErrAlreadyExists, and connection failures
// to ErrDBUnavailable.
func wrap(err error) error {
	var me *gomysql.MySQLError
	var ne net.Error
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: %v", ports.ErrNotFound, err)
	case errors.As(err, &me) && me.Number == errDupEntry:
		return fmt.Errorf("%w: %v", ports.ErrAlreadyExists, err)
	case errors.As(err, &me) && me.Number == errLockWaitTimeout:
		return fmt.Errorf("%w: %v", ports.ErrLockTimeout, err)
	case errors.As(err, &me) && me.Number == errDeadlock:
		return fmt.Errorf("%w: %v", ports.ErrDeadlock, err)
	case errors.Is(err, driver.ErrBadConn), errors.Is(err, gomysql.ErrInvalidConn), errors.Is(err, sql.ErrConnDone),
		errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.ECONNRESET), errors.As(err, &ne):
		return fmt.Errorf("%w: %v", ports.ErrDBUnavailable, err)
	}
	return err
}

// nullBytes stores "" as NULL in a nullable VARBINARY column.
func nullBytes(s string) any {
	if s == "" {
		return nil
	}
	return []byte(s)
}

// nullString stores "" as NULL in a nullable CHAR column.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func parseHash(algo, hex string) (contenthash.Hash, error) {
	h, err := contenthash.ParseHex(algo, hex)
	if err != nil {
		return contenthash.Hash{}, fmt.Errorf("mysql: stored hash: %w", err)
	}
	return h, nil
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?, ", n), ", ") }

// batch is the number of values in one IN (...) list.
const batch = 500

// ---- projects ---------------------------------------------------------------

// InsertProject implements ports.ProjectRepo.
func (s *Store) InsertProject(ctx context.Context, tx ports.Tx, p ports.Project) error {
	q, err := s.writer(tx)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO projects (project_id, tenant_id, name, status, owner_ref, owner_principal_uid,
		created_at_us, updated_at_us) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.TenantID, p.Name, p.Status, nullBytes(p.OwnerRef), nullString(p.OwnerPrincipalUID), p.CreatedAtUs, p.UpdatedAtUs)
	return wrap(err)
}

// GetProject implements ports.ProjectRepo.
func (s *Store) GetProject(ctx context.Context, tx ports.Tx, id string) (ports.Project, error) {
	q, err := s.reader(tx)
	if err != nil {
		return ports.Project{}, err
	}
	var p ports.Project
	var owner []byte
	var uid sql.NullString
	err = q.QueryRowContext(ctx, `SELECT project_id, tenant_id, name, status, owner_ref, owner_principal_uid, created_at_us,
		updated_at_us FROM projects WHERE project_id = ?`, id).
		Scan(&p.ID, &p.TenantID, &p.Name, &p.Status, &owner, &uid, &p.CreatedAtUs, &p.UpdatedAtUs)
	if err != nil {
		return ports.Project{}, wrap(err)
	}
	p.OwnerRef, p.OwnerPrincipalUID = string(owner), uid.String
	return p, nil
}

// ---- workspaces -------------------------------------------------------------

// InsertWorkspace implements ports.WorkspaceRepo.
func (s *Store) InsertWorkspace(ctx context.Context, tx ports.Tx, w ports.Workspace) error {
	q, err := s.writer(tx)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO workspaces (workspace_id, tenant_id, project_id, workspace_type, owner_ref,
		owner_principal_uid, owner_type, storage_backend_id, storage_prefix, storage_prefix_key, display_name, status,
		created_at_us, updated_at_us) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.TenantID, w.ProjectID, w.Type, []byte(w.OwnerRef), nullString(w.OwnerPrincipalUID), nullString(w.OwnerType),
		w.StorageBackendID, []byte(w.StoragePrefix), w.StoragePrefixKey, w.DisplayName, w.Status, w.CreatedAtUs, w.UpdatedAtUs)
	return wrap(err)
}

// GetWorkspace implements ports.WorkspaceRepo.
func (s *Store) GetWorkspace(ctx context.Context, tx ports.Tx, id string) (ports.Workspace, error) {
	q, err := s.reader(tx)
	if err != nil {
		return ports.Workspace{}, err
	}
	var w ports.Workspace
	var owner, prefix []byte
	var uid, ownerType sql.NullString
	err = q.QueryRowContext(ctx, `SELECT workspace_id, tenant_id, project_id, workspace_type, owner_ref, owner_principal_uid,
		owner_type, storage_backend_id, storage_prefix, storage_prefix_key, display_name, status, created_at_us, updated_at_us
		FROM workspaces WHERE workspace_id = ?`, id).
		Scan(&w.ID, &w.TenantID, &w.ProjectID, &w.Type, &owner, &uid, &ownerType, &w.StorageBackendID, &prefix, &w.StoragePrefixKey,
			&w.DisplayName, &w.Status, &w.CreatedAtUs, &w.UpdatedAtUs)
	if err != nil {
		return ports.Workspace{}, wrap(err)
	}
	w.OwnerRef, w.OwnerPrincipalUID, w.OwnerType, w.StoragePrefix = string(owner), uid.String, ownerType.String, string(prefix)
	return w, nil
}

// ---- branches ---------------------------------------------------------------

const branchCols = `branch_id, workspace_id, project_id, workstream_id, branch_name, ref_name, branch_type, head_snapshot_id,
	generation, status, created_by_ref, created_by_principal_uid, created_at_us, updated_at_us`

type scanner interface{ Scan(dest ...any) error }

func scanBranch(sc scanner) (ports.Branch, error) {
	var b ports.Branch
	var workstream, head, uid sql.NullString
	var name, ref, creator []byte
	if err := sc.Scan(&b.ID, &b.WorkspaceID, &b.ProjectID, &workstream, &name, &ref, &b.Type, &head, &b.Generation, &b.Status,
		&creator, &uid, &b.CreatedAtUs, &b.UpdatedAtUs); err != nil {
		return ports.Branch{}, err
	}
	b.WorkstreamID, b.HeadSnapshotID, b.CreatedByPrincipalUID = workstream.String, head.String, uid.String
	b.Name, b.RefName, b.CreatedByRef = string(name), string(ref), string(creator)
	return b, nil
}

// InsertBranch implements ports.BranchRepo.
func (s *Store) InsertBranch(ctx context.Context, tx ports.Tx, b ports.Branch) error {
	q, err := s.writer(tx)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO branches (`+branchCols+`) VALUES (`+placeholders(14)+`)`,
		b.ID, b.WorkspaceID, b.ProjectID, nullString(b.WorkstreamID), []byte(b.Name), []byte(b.RefName), b.Type,
		nullString(b.HeadSnapshotID), b.Generation, b.Status, []byte(b.CreatedByRef), nullString(b.CreatedByPrincipalUID),
		b.CreatedAtUs, b.UpdatedAtUs)
	return wrap(err)
}

// GetBranch implements ports.BranchRepo.
func (s *Store) GetBranch(ctx context.Context, tx ports.Tx, id string) (ports.Branch, error) {
	q, err := s.reader(tx)
	if err != nil {
		return ports.Branch{}, err
	}
	b, err := scanBranch(q.QueryRowContext(ctx, `SELECT `+branchCols+` FROM branches WHERE branch_id = ?`, id))
	return b, wrap(err)
}

// GetBranchByRef implements ports.BranchRepo.
func (s *Store) GetBranchByRef(ctx context.Context, tx ports.Tx, workspaceID, refName string) (ports.Branch, error) {
	q, err := s.reader(tx)
	if err != nil {
		return ports.Branch{}, err
	}
	b, err := scanBranch(q.QueryRowContext(ctx, `SELECT `+branchCols+` FROM branches WHERE workspace_id = ? AND ref_name = ?`,
		workspaceID, []byte(refName)))
	return b, wrap(err)
}

// ListBranches implements ports.BranchRepo.
func (s *Store) ListBranches(ctx context.Context, tx ports.Tx, workspaceID, afterRef string, limit int) ([]ports.Branch, error) {
	q, err := s.reader(tx)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT `+branchCols+` FROM branches WHERE workspace_id = ? AND ref_name > ?
		ORDER BY ref_name LIMIT ?`, workspaceID, []byte(afterRef), limit)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()
	var out []ports.Branch
	for rows.Next() {
		b, err := scanBranch(rows)
		if err != nil {
			return nil, wrap(err)
		}
		out = append(out, b)
	}
	return out, wrap(rows.Err())
}

// UpdateBranchHead implements ports.BranchRepo: the compare-and-swap on
// (branch_id, workspace_id, generation).
func (s *Store) UpdateBranchHead(ctx context.Context, tx ports.Tx, branchID, workspaceID string, expectedGeneration int64,
	headSnapshotID string, updatedAtUs int64) (bool, error) {
	q, err := s.writer(tx)
	if err != nil {
		return false, err
	}
	res, err := q.ExecContext(ctx, `UPDATE branches SET head_snapshot_id = ?, generation = generation + 1, updated_at_us = ?
		WHERE branch_id = ? AND workspace_id = ? AND generation = ?`,
		nullString(headSnapshotID), updatedAtUs, branchID, workspaceID, expectedGeneration)
	if err != nil {
		return false, wrap(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, wrap(err)
	}
	return n == 1, nil
}

// ---- usage ------------------------------------------------------------------

// InsertUsage implements ports.UsageRepo.
func (s *Store) InsertUsage(ctx context.Context, tx ports.Tx, workspaceID string, updatedAtUs int64) error {
	q, err := s.writer(tx)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO workspace_usage (workspace_id, used_bytes, reserved_bytes, file_count,
		snapshot_count, version, updated_at_us) VALUES (?, 0, 0, 0, 0, 1, ?)`, workspaceID, updatedAtUs)
	return wrap(err)
}

// ---- snapshots --------------------------------------------------------------

const snapshotCols = `snapshot_id, workspace_id, project_id, branch_id, ref_name, author_ref, author_principal_uid, author_type,
	on_behalf_of_ref, on_behalf_of_type, message, trigger_kind, root_tree_hash, root_tree_hash_algo, tree_format_version,
	entry_count, total_bytes, created_at_us`

func scanSnapshot(sc scanner) (ports.Snapshot, error) {
	var x ports.Snapshot
	var branch, uid, authorType, behalfType sql.NullString
	var ref, author, behalf []byte
	var root, algo string
	if err := sc.Scan(&x.ID, &x.WorkspaceID, &x.ProjectID, &branch, &ref, &author, &uid, &authorType, &behalf, &behalfType,
		&x.Message, &x.TriggerKind, &root, &algo, &x.TreeFormatVersion, &x.EntryCount, &x.TotalBytes, &x.CreatedAtUs); err != nil {
		return ports.Snapshot{}, err
	}
	h, err := parseHash(algo, root)
	if err != nil {
		return ports.Snapshot{}, err
	}
	x.BranchID, x.AuthorPrincipalUID, x.AuthorType, x.RootTreeHash = branch.String, uid.String, authorType.String, h
	x.RefName, x.AuthorRef, x.OnBehalfOfRef, x.OnBehalfOfType = string(ref), string(author), string(behalf), behalfType.String
	x.ParentIDs = []string{}
	return x, nil
}

// InsertSnapshot implements ports.SnapshotRepo.
func (s *Store) InsertSnapshot(ctx context.Context, tx ports.Tx, x ports.Snapshot) error {
	q, err := s.writer(tx)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO snapshots (snapshot_id, workspace_id, project_id, branch_id, ref_name, author_ref,
		author_principal_uid, author_type, on_behalf_of_ref, on_behalf_of_type, message, trigger_kind, root_tree_hash,
		root_tree_hash_algo, tree_format_version, parent_count, entry_count, total_bytes, status, created_at_us)
		VALUES (`+placeholders(20)+`)`,
		x.ID, x.WorkspaceID, x.ProjectID, nullString(x.BranchID), []byte(x.RefName), []byte(x.AuthorRef),
		nullString(x.AuthorPrincipalUID), nullString(x.AuthorType), nullBytes(x.OnBehalfOfRef), nullString(x.OnBehalfOfType),
		x.Message, x.TriggerKind, x.RootTreeHash.Hex(), x.RootTreeHash.Algorithm(), x.TreeFormatVersion, len(x.ParentIDs),
		x.EntryCount, x.TotalBytes, "committed", x.CreatedAtUs)
	if err != nil {
		return wrap(err)
	}
	if len(x.ParentIDs) == 0 {
		return nil
	}
	args := make([]any, 0, 3*len(x.ParentIDs))
	for i, p := range x.ParentIDs {
		args = append(args, x.ID, p, i)
	}
	_, err = q.ExecContext(ctx, `INSERT INTO snapshot_parents (snapshot_id, parent_id, parent_order) VALUES `+
		strings.TrimSuffix(strings.Repeat("(?, ?, ?), ", len(x.ParentIDs)), ", "), args...)
	return wrap(err)
}

// GetSnapshot implements ports.SnapshotRepo.
func (s *Store) GetSnapshot(ctx context.Context, tx ports.Tx, id string) (ports.Snapshot, error) {
	q, err := s.reader(tx)
	if err != nil {
		return ports.Snapshot{}, err
	}
	x, err := scanSnapshot(q.QueryRowContext(ctx, `SELECT `+snapshotCols+` FROM snapshots WHERE snapshot_id = ?`, id))
	if err != nil {
		return ports.Snapshot{}, wrap(err)
	}
	snaps := []*ports.Snapshot{&x}
	if err := s.loadParents(ctx, q, snaps); err != nil {
		return ports.Snapshot{}, err
	}
	return x, nil
}

// loadParents fills ParentIDs, in parent order.
func (s *Store) loadParents(ctx context.Context, q querier, snaps []*ports.Snapshot) error {
	byID := make(map[string]*ports.Snapshot, len(snaps))
	ids := make([]any, 0, len(snaps))
	for _, x := range snaps {
		byID[x.ID] = x
		ids = append(ids, x.ID)
	}
	for start := 0; start < len(ids); start += batch {
		chunk := ids[start:min(start+batch, len(ids))]
		rows, err := q.QueryContext(ctx, `SELECT snapshot_id, parent_id FROM snapshot_parents WHERE snapshot_id IN (`+
			placeholders(len(chunk))+`) ORDER BY snapshot_id, parent_order`, chunk...)
		if err != nil {
			return wrap(err)
		}
		for rows.Next() {
			var id, parent string
			if err := rows.Scan(&id, &parent); err != nil {
				rows.Close()
				return wrap(err)
			}
			if x := byID[id]; x != nil {
				x.ParentIDs = append(x.ParentIDs, parent)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return wrap(err)
		}
	}
	return nil
}

// ListSnapshots implements ports.SnapshotRepo: newest first, with the
// keyset condition (created_at_us, snapshot_id) < cursor written out so
// that both servers use the (workspace_id[, branch_id], created_at_us)
// index.
func (s *Store) ListSnapshots(ctx context.Context, tx ports.Tx, sq ports.SnapshotQuery) ([]ports.Snapshot, error) {
	q, err := s.reader(tx)
	if err != nil {
		return nil, err
	}
	where := []string{"workspace_id = ?"}
	args := []any{sq.WorkspaceID}
	if sq.BranchID != "" {
		where = append(where, "branch_id = ?")
		args = append(args, sq.BranchID)
	}
	if c := sq.Before; c != nil {
		where = append(where, "(created_at_us < ? OR (created_at_us = ? AND snapshot_id < ?))")
		args = append(args, c.CreatedAtUs, c.CreatedAtUs, c.SnapshotID)
	}
	args = append(args, sq.Limit)
	rows, err := q.QueryContext(ctx, `SELECT `+snapshotCols+` FROM snapshots WHERE `+strings.Join(where, " AND ")+
		` ORDER BY created_at_us DESC, snapshot_id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, wrap(err)
	}
	var out []ports.Snapshot
	for rows.Next() {
		x, err := scanSnapshot(rows)
		if err != nil {
			rows.Close()
			return nil, wrap(err)
		}
		out = append(out, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, wrap(err)
	}
	ptrs := make([]*ports.Snapshot, len(out))
	for i := range out {
		ptrs[i] = &out[i]
	}
	if err := s.loadParents(ctx, q, ptrs); err != nil {
		return nil, err
	}
	return out, nil
}

// ---- blobs ------------------------------------------------------------------

// InsertBlob implements ports.BlobRepo.
func (s *Store) InsertBlob(ctx context.Context, tx ports.Tx, b ports.Blob) error {
	q, err := s.writer(tx)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO blobs (workspace_id, blob_hash, hash_algo, size_bytes, storage_key, status,
		created_at_us, chunking) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		b.WorkspaceID, b.Hash.Hex(), b.Hash.Algorithm(), b.Size, []byte(b.StorageKey), b.Status, b.CreatedAtUs, nullString(b.Chunking))
	return wrap(err)
}

func scanBlob(sc scanner) (ports.Blob, error) {
	var b ports.Blob
	var hex, algo string
	var key []byte
	var layout sql.NullString
	if err := sc.Scan(&b.WorkspaceID, &hex, &algo, &b.Size, &key, &b.Status, &b.CreatedAtUs, &layout); err != nil {
		return ports.Blob{}, err
	}
	h, err := parseHash(algo, hex)
	if err != nil {
		return ports.Blob{}, err
	}
	b.Hash, b.StorageKey, b.Chunking = h, string(key), layout.String
	return b, nil
}

const blobCols = `workspace_id, blob_hash, hash_algo, size_bytes, storage_key, status, created_at_us, chunking`

// GetBlob implements ports.BlobRepo.
func (s *Store) GetBlob(ctx context.Context, tx ports.Tx, workspaceID string, h contenthash.Hash) (ports.Blob, error) {
	q, err := s.reader(tx)
	if err != nil {
		return ports.Blob{}, err
	}
	b, err := scanBlob(q.QueryRowContext(ctx, `SELECT `+blobCols+` FROM blobs WHERE workspace_id = ? AND blob_hash = ?`,
		workspaceID, h.Hex()))
	return b, wrap(err)
}

// GetBlobs implements ports.BlobRepo.
func (s *Store) GetBlobs(ctx context.Context, tx ports.Tx, workspaceID string, hashes []contenthash.Hash) (map[contenthash.Hash]ports.Blob, error) {
	q, err := s.reader(tx)
	if err != nil {
		return nil, err
	}
	out := map[contenthash.Hash]ports.Blob{}
	for start := 0; start < len(hashes); start += batch {
		chunk := hashes[start:min(start+batch, len(hashes))]
		args := make([]any, 0, len(chunk)+1)
		args = append(args, workspaceID)
		for _, h := range chunk {
			args = append(args, h.Hex())
		}
		rows, err := q.QueryContext(ctx, `SELECT `+blobCols+` FROM blobs WHERE workspace_id = ? AND blob_hash IN (`+
			placeholders(len(chunk))+`)`, args...)
		if err != nil {
			return nil, wrap(err)
		}
		for rows.Next() {
			b, err := scanBlob(rows)
			if err != nil {
				rows.Close()
				return nil, wrap(err)
			}
			out[b.Hash] = b
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, wrap(err)
		}
	}
	return out, nil
}

// MarkBlobsLive implements ports.BlobRepo.
func (s *Store) MarkBlobsLive(ctx context.Context, tx ports.Tx, workspaceID string, hashes []contenthash.Hash) error {
	q, err := s.writer(tx)
	if err != nil {
		return err
	}
	for start := 0; start < len(hashes); start += batch {
		chunk := hashes[start:min(start+batch, len(hashes))]
		args := make([]any, 0, len(chunk)+3)
		args = append(args, ports.BlobLive, workspaceID, ports.BlobStaged)
		for _, h := range chunk {
			args = append(args, h.Hex())
		}
		if _, err := q.ExecContext(ctx, `UPDATE blobs SET status = ? WHERE workspace_id = ? AND status = ? AND blob_hash IN (`+
			placeholders(len(chunk))+`)`, args...); err != nil {
			return wrap(err)
		}
	}
	return nil
}

// ---- idempotency ------------------------------------------------------------

// InsertIdempotency implements ports.IdempotencyRepo.
func (s *Store) InsertIdempotency(ctx context.Context, tx ports.Tx, rec ports.IdempotencyRecord) error {
	q, err := s.writer(tx)
	if err != nil {
		return err
	}
	var response any
	if rec.Response != nil {
		response = string(rec.Response)
	}
	_, err = q.ExecContext(ctx, `INSERT INTO idempotency_keys (idempotency_key, actor_ref, actor_type, route, request_hash,
		response_json, status, created_at_us, expires_at_us) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		[]byte(rec.Key), []byte(rec.ActorRef), nullString(rec.ActorType), rec.Route, rec.RequestHash, response, rec.Status,
		rec.CreatedAtUs, rec.ExpiresAtUs)
	return wrap(err)
}

// GetIdempotency implements ports.IdempotencyRepo.
func (s *Store) GetIdempotency(ctx context.Context, tx ports.Tx, actorRef, route, key string) (ports.IdempotencyRecord, error) {
	q, err := s.reader(tx)
	if err != nil {
		return ports.IdempotencyRecord{}, err
	}
	rec := ports.IdempotencyRecord{ActorRef: actorRef, Route: route, Key: key}
	var response, actorType sql.NullString
	err = q.QueryRowContext(ctx, `SELECT actor_type, request_hash, response_json, status, created_at_us, expires_at_us
		FROM idempotency_keys WHERE actor_ref = ? AND route = ? AND idempotency_key = ?`, []byte(actorRef), route, []byte(key)).
		Scan(&actorType, &rec.RequestHash, &response, &rec.Status, &rec.CreatedAtUs, &rec.ExpiresAtUs)
	if err != nil {
		return ports.IdempotencyRecord{}, wrap(err)
	}
	rec.ActorType = actorType.String
	if response.Valid {
		rec.Response = []byte(response.String)
	}
	return rec, nil
}

// CompleteIdempotency implements ports.IdempotencyRepo.
func (s *Store) CompleteIdempotency(ctx context.Context, tx ports.Tx, actorRef, route, key string, createdAtUs int64, response []byte) error {
	q, err := s.writer(tx)
	if err != nil {
		return err
	}
	res, err := q.ExecContext(ctx, `UPDATE idempotency_keys SET status = ?, response_json = ? WHERE actor_ref = ? AND route = ?
		AND idempotency_key = ? AND created_at_us = ? AND status = ?`, ports.IdempotencyCompleted, string(response),
		[]byte(actorRef), route, []byte(key), createdAtUs, ports.IdempotencyInProgress)
	if err != nil {
		return wrap(err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("%w: in-progress idempotency record (%d rows, %v)", ports.ErrNotFound, n, err)
	}
	return nil
}

// DeleteIdempotency implements ports.IdempotencyRepo.
func (s *Store) DeleteIdempotency(ctx context.Context, tx ports.Tx, actorRef, route, key string, createdAtUs int64, status string) error {
	q, err := s.writer(tx)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE actor_ref = ? AND route = ? AND idempotency_key = ?
		AND created_at_us = ? AND status = ?`, []byte(actorRef), route, []byte(key), createdAtUs, status)
	return wrap(err)
}

// ---- outbox -----------------------------------------------------------------

// Append implements ports.EventSink: the row is written with status
// pending in the caller's transaction.
func (s *Store) Append(ctx context.Context, tx ports.Tx, ev ports.Event) error {
	q, err := s.writer(tx)
	if err != nil {
		return err
	}
	if ev.ID == "" || ev.Type == "" {
		return fmt.Errorf("mysql: event needs an ID and a type")
	}
	_, err = q.ExecContext(ctx, `INSERT INTO outbox_events (event_id, workspace_id, event_type, aggregate_id, payload_json, status,
		retry_count, created_at_us) VALUES (?, ?, ?, ?, ?, 'pending', 0, ?)`,
		ev.ID, nullString(ev.WorkspaceID), ev.Type, nullString(ev.AggregateID), string(ev.Payload), ev.CreatedAtUs)
	if err == nil {
		t := tx.(*txn)
		t.eventIDs = append(t.eventIDs, ev.ID)
	}
	return wrap(err)
}

// Events returns every outbox event, oldest first (tests and diagnostics).
func (s *Store) Events(ctx context.Context) ([]ports.Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT event_id, workspace_id, event_type, aggregate_id, payload_json, created_at_us
		FROM outbox_events ORDER BY commit_seq, created_at_us, event_id`)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()
	var out []ports.Event
	for rows.Next() {
		var ev ports.Event
		var ws, agg sql.NullString
		var payload string
		if err := rows.Scan(&ev.ID, &ws, &ev.Type, &agg, &payload, &ev.CreatedAtUs); err != nil {
			return nil, wrap(err)
		}
		ev.WorkspaceID, ev.AggregateID, ev.Payload = ws.String, agg.String, []byte(payload)
		out = append(out, ev)
	}
	return out, wrap(rows.Err())
}

// PendingEvents implements ports.OutboxQueue.
func (s *Store) PendingEvents(ctx context.Context, limit int) ([]ports.Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT event_id, workspace_id, event_type, aggregate_id, payload_json, created_at_us
		FROM outbox_events WHERE status = 'pending' AND event_type LIKE ? ORDER BY commit_seq, created_at_us, event_id LIMIT ?`,
		ports.EventPrefix+"%", limit)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()
	var out []ports.Event
	for rows.Next() {
		var ev ports.Event
		var ws, agg sql.NullString
		var payload string
		if err := rows.Scan(&ev.ID, &ws, &ev.Type, &agg, &payload, &ev.CreatedAtUs); err != nil {
			return nil, wrap(err)
		}
		ev.WorkspaceID, ev.AggregateID, ev.Payload = ws.String, agg.String, []byte(payload)
		out = append(out, ev)
	}
	return out, wrap(rows.Err())
}

// PendingCount implements ports.OutboxQueue.
func (s *Store) PendingCount(ctx context.Context) (n, oldestAtUs int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MIN(created_at_us), 0) FROM outbox_events
		WHERE status = 'pending' AND event_type LIKE ?`, ports.EventPrefix+"%").Scan(&n, &oldestAtUs)
	return n, oldestAtUs, wrap(err)
}

// MarkPublished implements ports.OutboxQueue.
func (s *Store) MarkPublished(ctx context.Context, id string, publishedAtUs int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE outbox_events SET status = 'published', published_at_us = ?
		WHERE event_id = ? AND status = 'pending'`, publishedAtUs, id)
	return wrap(err)
}

// MarkFailed implements ports.OutboxQueue.
func (s *Store) MarkFailed(ctx context.Context, id string, nextRetryAtUs int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE outbox_events SET retry_count = retry_count + 1, next_retry_at_us = ?
		WHERE event_id = ? AND status = 'pending'`, nextRetryAtUs, id)
	return wrap(err)
}

// EventDelivery returns the delivery state of an outbox event: its status,
// retry count and next retry time (tests and diagnostics).
func (s *Store) EventDelivery(ctx context.Context, id string) (ports.EventDelivery, error) {
	var d ports.EventDelivery
	var next, published sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT status, retry_count, next_retry_at_us, published_at_us FROM outbox_events
		WHERE event_id = ?`, id).Scan(&d.Status, &d.RetryCount, &next, &published)
	d.NextRetryAtUs, d.PublishedAtUs = next.Int64, published.Int64
	return d, wrap(err)
}
