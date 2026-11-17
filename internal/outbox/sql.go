package outbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// datetime6 is the form of a DATETIME(6) value: UTC, microseconds. Times
// are sent as text, so that the time zone of the connection (the loc
// parameter of a DSN) cannot shift them.
const datetime6 = "2006-01-02 15:04:05.000000"

// tableRe is the form of a table name, schema.table.
var tableRe = regexp.MustCompile(`^[A-Za-z0-9_]+\.[A-Za-z0-9_]+$`)

const (
	errDupEntry          = 1062 // ER_DUP_ENTRY
	errTableAccessDenied = 1142 // ER_TABLEACCESS_DENIED_ERROR
)

// SQLSink writes the host's outbox table over a MySQL-protocol connection
// (MySQL 8.0 or seekdb):
//
//	INSERT INTO <table> (event_id, module, event_kind, scope_kind, scope_id, actor_type, actor_id,
//	  payload_json, status, attempts, available_at, created_at, updated_at)
//	VALUES (?, 'mfgraph', ?, 'project', ?, ?, ?, ?, 'pending', 0, ?, ?, ?)
//
// A duplicate key error (1062) whose duplicate value is the row's event_id
// means the event was delivered before; the name of the unique key does not
// matter. The table must have a unique index on event_id
// (UniqueEventIDIndex): without one a delivered event that is delivered
// again (after a crash between the write and the mark) is written twice.
type SQLSink struct {
	db            *sql.DB
	schema, table string
	insert        string
	check         string
}

// NewSQLSink returns the sink of table (schema.table) on db.
func NewSQLSink(db *sql.DB, table string) (*SQLSink, error) {
	if !tableRe.MatchString(table) {
		return nil, fmt.Errorf("outbox: table %q is not schema.table", table)
	}
	schema, name, _ := strings.Cut(table, ".")
	quoted := "`" + schema + "`.`" + name + "`"
	return &SQLSink{
		db: db, schema: schema, table: name,
		insert: "INSERT INTO " + quoted + " (event_id, module, event_kind, scope_kind, scope_id, actor_type, actor_id, " +
			"payload_json, status, attempts, available_at, created_at, updated_at) " +
			"VALUES (?, 'mfgraph', ?, 'project', ?, ?, ?, ?, 'pending', 0, ?, ?, ?)",
		check: "SELECT 1 FROM " + quoted + " LIMIT 0",
	}, nil
}

// Insert implements Sink.
func (s *SQLSink) Insert(ctx context.Context, r Row) error {
	var actorType any
	if r.ActorType != nil {
		actorType = *r.ActorType
	}
	_, err := s.db.ExecContext(ctx, s.insert, r.EventID, r.EventKind, r.ScopeID, actorType, r.ActorID, string(r.Payload),
		r.AvailableAt.UTC().Format(datetime6), r.CreatedAt.UTC().Format(datetime6), r.UpdatedAt.UTC().Format(datetime6))
	if isDuplicateOf(err, r.EventID) {
		return nil
	}
	return err
}

// IndexState says whether the host table has a unique index on event_id.
type IndexState int

// IndexState values.
const (
	// IndexUnknown: information_schema shows no index of the table to the
	// forwarder's account, so it cannot tell.
	IndexUnknown IndexState = iota
	// IndexUnique: a unique index (or the primary key) on exactly event_id.
	IndexUnique
	// IndexMissing: the table's indexes are visible and none of them is a
	// unique index on exactly event_id.
	IndexMissing
)

func (s IndexState) String() string {
	switch s {
	case IndexUnique:
		return "unique"
	case IndexMissing:
		return "missing"
	}
	return "unknown"
}

// UniqueEventIDIndex looks in information_schema.statistics for a unique
// index whose only column is event_id. The host table has a primary key, so
// an account that sees no index at all cannot see them (IndexUnknown).
func (s *SQLSink) UniqueEventIDIndex(ctx context.Context) (IndexState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT index_name, non_unique, column_name FROM information_schema.statistics
		WHERE table_schema = ? AND table_name = ?`, s.schema, s.table)
	if err != nil {
		return IndexUnknown, err
	}
	defer rows.Close()
	type index struct {
		unique  bool
		columns []string
	}
	indexes := map[string]*index{}
	for rows.Next() {
		var name, column string
		var nonUnique int64
		if err := rows.Scan(&name, &nonUnique, &column); err != nil {
			return IndexUnknown, err
		}
		ix := indexes[name]
		if ix == nil {
			ix = &index{unique: nonUnique == 0}
			indexes[name] = ix
		}
		ix.columns = append(ix.columns, column)
	}
	if err := rows.Err(); err != nil {
		return IndexUnknown, err
	}
	if len(indexes) == 0 {
		return IndexUnknown, nil
	}
	for _, ix := range indexes {
		if ix.unique && len(ix.columns) == 1 && strings.EqualFold(ix.columns[0], "event_id") {
			return IndexUnique, nil
		}
	}
	return IndexMissing, nil
}

// Check implements Sink: the connection works and the table exists, by
// SELECT 1 FROM <table> LIMIT 0. The account of the forwarder may have
// INSERT on the table and nothing else, which MySQL and seekdb answer with
// a privilege error; the table must then be listed in
// information_schema.tables, which shows the tables an account has any
// privilege on.
func (s *SQLSink) Check(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, s.check)
	if err == nil {
		return rows.Close()
	}
	var me *mysql.MySQLError
	if !errors.As(err, &me) || me.Number != errTableAccessDenied {
		return err
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ? AND table_name = ?`,
		s.schema, s.table).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("outbox: table %s.%s not found", s.schema, s.table)
	}
	return nil
}

// isDuplicateOf reports a duplicate key error (1062) whose duplicate value
// is exactly eventID: MySQL 8.0 and seekdb quote it, as in
// "Duplicate entry '<value>' for key '<key>'". An event_id is 64 hex digits,
// so no other column of the row can have that value, and a key of several
// columns would quote them joined by '-'.
func isDuplicateOf(err error, eventID string) bool {
	var me *mysql.MySQLError
	return eventID != "" && errors.As(err, &me) && me.Number == errDupEntry && strings.Contains(me.Message, "'"+eventID+"'")
}

// Time reads a DATETIME(6) value written by Insert (tests and diagnostics).
func Time(s string) (time.Time, error) { return time.ParseInLocation(datetime6, s, time.UTC) }
