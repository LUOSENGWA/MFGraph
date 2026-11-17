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

// UniqueKey is the name of the host table's unique key on event_id.
const UniqueKey = "uk_module_outbox_event"

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
// A duplicate key on UniqueKey means the event was delivered before.
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
	if isDuplicateEvent(err) {
		return nil
	}
	return err
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

// isDuplicateEvent reports a duplicate key on the event_id of the host
// table.
func isDuplicateEvent(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == errDupEntry && strings.Contains(me.Message, UniqueKey)
}

// Time reads a DATETIME(6) value written by Insert (tests and diagnostics).
func Time(s string) (time.Time, error) { return time.ParseInLocation(datetime6, s, time.UTC) }
