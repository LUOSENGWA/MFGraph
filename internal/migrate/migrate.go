package migrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Component names used in schema_migrations.
const (
	ComponentKernel  = "kernel"
	ComponentMFGraph = "mfgraph"
)

// Migration is one immutable SQL file.
type Migration struct {
	Version  string // file name without ".sql", e.g. "0001_workspaces"
	Number   int    // numeric prefix, e.g. 1
	Checksum string // lowercase hex SHA-256 of the file bytes
	SQL      string
}

var (
	// ErrChecksumMismatch: an applied migration file was modified.
	ErrChecksumMismatch = errors.New("migrate: checksum of an applied migration changed")
	// ErrUnknownVersion: the database records a migration that is not in the
	// source (files must not be removed or renamed).
	ErrUnknownVersion = errors.New("migrate: applied migration missing from source")
	// ErrInvalidSource: a migration file is malformed or misnamed.
	ErrInvalidSource = errors.New("migrate: invalid migration source")
)

var namePattern = regexp.MustCompile(`^([0-9]{4})_[a-z0-9_]+\.sql$`)

// Load reads every *.sql file at the root of fsys, in file name order.
// Files must be named NNNN_description.sql (lowercase), numbers must be
// unique, and gaps are allowed. Other files (README.md, ...) are ignored.
func Load(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var out []Migration
	seen := map[int]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		m := namePattern.FindStringSubmatch(name)
		if m == nil || len(name)-len(".sql") > 64 {
			return nil, fmt.Errorf("%w: file name %q must match NNNN_description.sql (at most 64 characters before .sql)", ErrInvalidSource, name)
		}
		num, _ := strconv.Atoi(m[1])
		if prev, dup := seen[num]; dup {
			return nil, fmt.Errorf("%w: %q and %q share number %04d", ErrInvalidSource, prev, name, num)
		}
		seen[num] = name
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		out = append(out, Migration{
			Version:  strings.TrimSuffix(name, ".sql"),
			Number:   num,
			Checksum: hex.EncodeToString(sum[:]),
			SQL:      string(b),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// SplitStatements splits a migration into statements. A statement ends at a
// line whose last non-blank character is ';' (that is, ";" + newline). Lines
// whose first non-blank characters are "--" are comments and never end a
// statement. Comment-only chunks are dropped. This is safe because migrations
// contain no stored programs and no string literals spanning lines; inline
// comments must not end with ';'.
func SplitStatements(src string) ([]string, error) {
	var stmts []string
	var cur []string
	hasCode := false
	for _, line := range strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		isComment := strings.HasPrefix(trimmed, "--")
		if trimmed == "" && !hasCode {
			continue
		}
		cur = append(cur, line)
		if !isComment && trimmed != "" {
			hasCode = true
		}
		if !isComment && strings.HasSuffix(trimmed, ";") {
			stmts = append(stmts, strings.TrimSpace(strings.Join(cur, "\n")))
			cur, hasCode = nil, false
		}
	}
	if hasCode {
		return nil, fmt.Errorf("%w: trailing statement without ';'", ErrInvalidSource)
	}
	return stmts, nil
}

// Target is the database migrations are applied to.
type Target interface {
	// EnsureVersionTable creates schema_migrations if it does not exist.
	EnsureVersionTable(ctx context.Context) error
	// AppliedVersions returns version -> checksum for one component.
	AppliedVersions(ctx context.Context, component string) (map[string]string, error)
	// Exec runs one statement outside any transaction (DDL commits implicitly).
	Exec(ctx context.Context, stmt string) error
	// RecordVersion stores a successfully applied migration.
	RecordVersion(ctx context.Context, component, version, checksum string, appliedAtUs int64) error
	// AlreadyExists reports whether err, returned by Exec for an ALTER
	// TABLE ... ADD statement, says that the column or index exists.
	AlreadyExists(err error) bool
}

// isAlterAdd reports an ALTER TABLE ... ADD statement (one column or one
// index), which a repeated run may find already applied.
func isAlterAdd(stmt string) bool {
	f := strings.Fields(strings.ToUpper(firstLine(stmt)))
	return len(f) >= 4 && f[0] == "ALTER" && f[1] == "TABLE" && f[3] == "ADD"
}

// Result lists what Run did.
type Result struct {
	Applied []string
	Skipped []string
}

// Run applies the pending migrations of one component, in order.
//
//   - Before anything is executed, every applied version is checked: a
//     version whose file is gone fails with ErrUnknownVersion, a changed
//     file fails with ErrChecksumMismatch.
//   - A pending migration is executed statement by statement (no transaction:
//     MySQL DDL commits implicitly) and recorded only after all its statements
//     succeeded. Statements are CREATE TABLE IF NOT EXISTS, or ALTER TABLE ...
//     ADD of one column or one index, which is skipped when the column or
//     index exists (Target.AlreadyExists), so a run that stopped half-way can
//     simply be repeated.
//   - Lower numbers may be applied after higher ones (gaps can be filled).
//
// Run assumes a single runner at a time; it takes no lock.
func Run(ctx context.Context, t Target, component string, ms []Migration, now func() time.Time) (Result, error) {
	var res Result
	if err := t.EnsureVersionTable(ctx); err != nil {
		return res, fmt.Errorf("migrate: create schema_migrations: %w", err)
	}
	applied, err := t.AppliedVersions(ctx, component)
	if err != nil {
		return res, fmt.Errorf("migrate: read schema_migrations: %w", err)
	}
	byVersion := make(map[string]Migration, len(ms))
	for _, m := range ms {
		byVersion[m.Version] = m
	}
	versions := make([]string, 0, len(applied))
	for v := range applied {
		versions = append(versions, v)
	}
	sort.Strings(versions)
	for _, v := range versions {
		m, ok := byVersion[v]
		if !ok {
			return res, fmt.Errorf("%w: %s/%s", ErrUnknownVersion, component, v)
		}
		if m.Checksum != applied[v] {
			return res, fmt.Errorf("%w: %s/%s (recorded %s, file %s)", ErrChecksumMismatch, component, v, applied[v], m.Checksum)
		}
	}
	for _, m := range ms {
		if _, ok := applied[m.Version]; ok {
			res.Skipped = append(res.Skipped, m.Version)
			continue
		}
		stmts, err := SplitStatements(m.SQL)
		if err != nil {
			return res, fmt.Errorf("migrate: %s/%s: %w", component, m.Version, err)
		}
		if len(stmts) == 0 {
			return res, fmt.Errorf("%w: %s/%s has no statements", ErrInvalidSource, component, m.Version)
		}
		for i, s := range stmts {
			if err := t.Exec(ctx, s); err != nil {
				if isAlterAdd(s) && t.AlreadyExists(err) {
					continue // applied by an earlier, interrupted run
				}
				return res, fmt.Errorf("migrate: %s/%s statement %d (%s): %w", component, m.Version, i+1, firstLine(s), err)
			}
		}
		if err := t.RecordVersion(ctx, component, m.Version, m.Checksum, now().UnixMicro()); err != nil {
			return res, fmt.Errorf("migrate: record %s/%s: %w", component, m.Version, err)
		}
		res.Applied = append(res.Applied, m.Version)
	}
	return res, nil
}

func firstLine(stmt string) string {
	for _, l := range strings.Split(stmt, "\n") {
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "--") {
			return t
		}
	}
	return ""
}

// VersionTableDDL creates the bookkeeping table: one table for all
// components, keyed by (component, version). It uses utf8mb4_bin rather than
// the ascii character set so that it runs on MySQL 8.0 and on seekdb alike.
const VersionTableDDL = `CREATE TABLE IF NOT EXISTS schema_migrations (
  component     VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  version       VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  checksum      CHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  applied_at_us BIGINT NOT NULL,
  PRIMARY KEY (component, version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`

// SQLTarget applies migrations through database/sql (MySQL dialect).
type SQLTarget struct{ DB *sql.DB }

// EnsureVersionTable implements Target.
func (t SQLTarget) EnsureVersionTable(ctx context.Context) error {
	_, err := t.DB.ExecContext(ctx, VersionTableDDL)
	return err
}

// AppliedVersions implements Target.
func (t SQLTarget) AppliedVersions(ctx context.Context, component string) (map[string]string, error) {
	rows, err := t.DB.QueryContext(ctx, "SELECT version, checksum FROM schema_migrations WHERE component = ?", component)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var v, c string
		if err := rows.Scan(&v, &c); err != nil {
			return nil, err
		}
		out[v] = c
	}
	return out, rows.Err()
}

// Exec implements Target.
func (t SQLTarget) Exec(ctx context.Context, stmt string) error {
	_, err := t.DB.ExecContext(ctx, stmt)
	return err
}

// MySQL error numbers of an ADD whose column or index exists:
// ER_DUP_FIELDNAME and ER_DUP_KEYNAME (seekdb reports the same numbers).
const (
	errDupFieldName = 1060
	errDupKeyName   = 1061
)

// AlreadyExists implements Target.
func (t SQLTarget) AlreadyExists(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && (me.Number == errDupFieldName || me.Number == errDupKeyName)
}

// RecordVersion implements Target.
func (t SQLTarget) RecordVersion(ctx context.Context, component, version, checksum string, appliedAtUs int64) error {
	_, err := t.DB.ExecContext(ctx,
		"INSERT INTO schema_migrations (component, version, checksum, applied_at_us) VALUES (?, ?, ?, ?)",
		component, version, checksum, appliedAtUs)
	return err
}

// Plan is one component's migrations.
type Plan struct {
	Component string
	Source    fs.FS
}

// RunAll loads and runs several components in order (for example kernel,
// then mfgraph) and returns one Result per component.
func RunAll(ctx context.Context, t Target, plans []Plan, now func() time.Time) ([]Result, error) {
	var out []Result
	for _, p := range plans {
		ms, err := Load(p.Source)
		if err != nil {
			return out, fmt.Errorf("migrate: load %s: %w", p.Component, err)
		}
		r, err := Run(ctx, t, p.Component, ms, now)
		out = append(out, r)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}
