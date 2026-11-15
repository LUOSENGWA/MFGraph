package migrations_test

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/migrate"
	"github.com/LUOSENGWA/MFGraph/migrations"
)

// The schema frozen for the first core slice. Tables of later slices (locks,
// packs, chunks, snapshot_entries, project commits, ...) must not appear yet.
var wantTables = map[string][]string{
	"kernel": {"principals", "projects"},
	"mysql": {"blobs", "branches", "idempotency_keys", "outbox_events", "snapshot_parents", "snapshots",
		"upload_parts", "upload_sessions", "workspace_usage", "workspaces", "workstreams"},
}

var wantVersions = map[string][]string{
	"kernel": {"0000_bootstrap"},
	"mysql": {"0001_workspaces", "0002_workstreams_branches", "0003_snapshots", "0004_blobs",
		"0006_uploads", "0008_usage_outbox_idempotency"},
}

// byteColumns hold names, paths, refs and keys that are compared byte for
// byte. They must be VARBINARY: utf8mb4_bin (a PAD SPACE collation) ignores
// trailing spaces, so "dev" and "dev " would be the same branch, while the
// path rules allow trailing spaces. The lengths follow the path rules: a name
// has at most 255 bytes, a path 4096, a ref "workstreams/<id>/<name>" 294
// (512 leaves room), a storage prefix 942.
var byteColumns = map[string]string{
	"branches.branch_name":             "VARBINARY(255)",
	"branches.ref_name":                "VARBINARY(512)",
	"snapshots.ref_name":               "VARBINARY(512)",
	"workstreams.default_branch_name":  "VARBINARY(255)",
	"workspaces.scope_path":            "VARBINARY(4096)",
	"snapshots.scope_path":             "VARBINARY(4096)",
	"upload_sessions.path":             "VARBINARY(4096)",
	"workspaces.storage_prefix":        "VARBINARY(942)",
	"blobs.storage_key":                "VARBINARY(1024)",
	"idempotency_keys.idempotency_key": "VARBINARY(128)",
}

// actorColumns record who did something. They are actor references
// (VARBINARY(255), stored as given and compared byte for byte), never ULID
// columns, because a host platform identifies callers by its own strings.
// Each has an optional sibling <name without _ref>_principal_uid CHAR(26)
// NULL for a ULID the host platform may provide later; the idempotency
// record needs none. The value is the required nullability.
var actorColumns = map[string]string{
	"workspaces.owner_ref":           "NOT NULL",
	"workstreams.owner_ref":          "NOT NULL",
	"workstreams.created_by_ref":     "NOT NULL",
	"branches.created_by_ref":        "NOT NULL",
	"snapshots.author_ref":           "NOT NULL",
	"snapshots.on_behalf_of_ref":     "NULL",
	"upload_sessions.created_by_ref": "NOT NULL",
	"idempotency_keys.actor_ref":     "NOT NULL",
}

var (
	createRe = regexp.MustCompile(`^CREATE TABLE IF NOT EXISTS ([a-z_]+) \(`)
	columnRe = regexp.MustCompile(`^\s+([a-z_]+)\s+([A-Z]+(?:\([0-9]+\))?)(.*)$`)
	// Constructs outside the portable MySQL subset (no ENUM/SET/JSON types,
	// generated columns, foreign keys, triggers, stored programs, DATETIME or
	// TIMESTAMP, 8.0-only collations, CHECK constraints, ...).
	forbidden = regexp.MustCompile(`(?i)\b(ENUM|SET\s*\(|JSON|GENERATED|AUTO_INCREMENT|FOREIGN\s+KEY|REFERENCES|TRIGGER|PROCEDURE|FUNCTION|DATETIME|TIMESTAMP|RETURNING|utf8mb4_0900\w*|CHECK|SKIP\s+LOCKED|ON\s+UPDATE|PARTITION)\b`)
	// seekdb supports only the utf8mb4 and binary character sets: every
	// column-level character set is utf8mb4 with utf8mb4_bin.
	charsetRe   = regexp.MustCompile(`(?i)\b(CHARACTER\s+SET|CHARSET|COLLATE)\s*=?\s*([a-z0-9_]+)`)
	tableSuffix = ") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;"
)

func TestMigrationFiles(t *testing.T) {
	sources := map[string]fs.FS{"kernel": migrations.Kernel(), "mysql": migrations.MySQL()}
	seenTables := map[string]string{}
	seenByteColumns := map[string]bool{}
	seenActorColumns := map[string]bool{}
	for dir, fsys := range sources {
		ms, err := migrate.Load(fsys)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		var versions []string
		var tables []string
		for _, m := range ms {
			versions = append(versions, m.Version)
			if strings.Contains(m.SQL, "\r") {
				t.Errorf("%s/%s: CR line endings (checksums need LF)", dir, m.Version)
			}
			for _, line := range strings.Split(m.SQL, "\n") {
				if i := strings.Index(line, "--"); i > 0 && strings.HasSuffix(strings.TrimSpace(line[i:]), ";") {
					t.Errorf("%s/%s: inline comment ends with ';': %q", dir, m.Version, line)
				}
			}
			stmts, err := migrate.SplitStatements(m.SQL)
			if err != nil {
				t.Fatalf("%s/%s: %v", dir, m.Version, err)
			}
			for _, s := range stmts {
				body := stripComments(s)
				mm := createRe.FindStringSubmatch(body)
				if mm == nil {
					t.Errorf("%s/%s: only CREATE TABLE IF NOT EXISTS is allowed: %q", dir, m.Version, firstLine(body))
					continue
				}
				table := mm[1]
				if prev, dup := seenTables[table]; dup {
					t.Errorf("table %s defined in %s and %s", table, prev, m.Version)
				}
				seenTables[table] = m.Version
				tables = append(tables, table)
				if !strings.HasSuffix(body, tableSuffix) {
					t.Errorf("%s: table %s must end with %q", m.Version, table, tableSuffix)
				}
				if f := forbidden.FindString(body); f != "" {
					t.Errorf("%s: table %s uses forbidden construct %q", m.Version, table, f)
				}
				for _, cs := range charsetRe.FindAllStringSubmatch(body, -1) {
					if v := strings.ToLower(cs[2]); v != "utf8mb4" && v != "utf8mb4_bin" {
						t.Errorf("%s: table %s uses %s %s; only utf8mb4 / utf8mb4_bin run on MySQL 8.0 and seekdb",
							m.Version, table, cs[1], cs[2])
					}
				}
				checkColumns(t, m.Version, table, body, seenByteColumns)
				checkActorColumns(t, m.Version, table, body, seenActorColumns)
			}
		}
		if strings.Join(versions, ",") != strings.Join(wantVersions[dir], ",") {
			t.Errorf("%s versions = %v, want %v", dir, versions, wantVersions[dir])
		}
		sort.Strings(tables)
		if strings.Join(tables, ",") != strings.Join(wantTables[dir], ",") {
			t.Errorf("%s tables = %v, want %v", dir, tables, wantTables[dir])
		}
	}
	for col := range byteColumns {
		if !seenByteColumns[col] {
			t.Errorf("column %s not found", col)
		}
	}
	for col := range actorColumns {
		if !seenActorColumns[col] {
			t.Errorf("actor column %s not found", col)
		}
	}
}

type column struct{ typ, rest string }

func columns(body string) map[string]column {
	out := map[string]column{}
	for _, line := range strings.Split(body, "\n") {
		m := columnRe.FindStringSubmatch(line)
		if m == nil || m[1] == "key" || m[1] == "primary" || m[1] == "unique" {
			continue
		}
		out[m[1]] = column{typ: m[2], rest: m[3]}
	}
	return out
}

func nullability(rest string) string {
	if strings.Contains(rest, "NOT NULL") {
		return "NOT NULL"
	}
	return "NULL"
}

// checkActorColumns enforces actorColumns: *_ref columns are VARBINARY(255)
// with the listed nullability and a nullable CHAR(26) *_principal_uid
// sibling, and the old ULID-typed identity names (created_by,
// *_principal_id, on_behalf_of_id, principal_uid outside the kernel
// principals table) do not come back.
func checkActorColumns(t *testing.T, version, table, body string, seen map[string]bool) {
	t.Helper()
	cols := columns(body)
	for name, c := range cols {
		col := table + "." + name
		if name == "created_by" || name == "on_behalf_of_id" || strings.HasSuffix(name, "_principal_id") ||
			name == "principal_uid" && table != "principals" {
			t.Errorf("%s: %s: actors are actor references, name the column *_ref (plus *_principal_uid)", version, col)
		}
		if !strings.HasSuffix(name, "_ref") {
			continue
		}
		seen[col] = true
		want, ok := actorColumns[col]
		if !ok {
			t.Errorf("%s: %s is not listed in actorColumns", version, col)
			continue
		}
		if c.typ != "VARBINARY(255)" || nullability(c.rest) != want {
			t.Errorf("%s: %s must be VARBINARY(255) %s, is %s%s", version, col, want, c.typ, c.rest)
		}
		if col == "idempotency_keys.actor_ref" {
			continue
		}
		sib := strings.TrimSuffix(name, "_ref") + "_principal_uid"
		if u, ok := cols[sib]; !ok || u.typ != "CHAR(26)" || nullability(u.rest) != "NULL" {
			t.Errorf("%s: %s needs a nullable CHAR(26) sibling %s.%s", version, col, table, sib)
		}
	}
}

// checkColumns enforces the type conventions: IDs and hashes are utf8mb4_bin,
// times are BIGINT epoch microseconds, names, paths, refs and keys are
// VARBINARY (byteColumns).
func checkColumns(t *testing.T, version, table, body string, seenByteColumns map[string]bool) {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		m := columnRe.FindStringSubmatch(line)
		if m == nil || m[1] == "key" || m[1] == "primary" || m[1] == "unique" {
			continue
		}
		name, typ, rest := m[1], m[2], m[3]
		if typ == "CHAR(26)" || typ == "CHAR(64)" {
			if !strings.Contains(rest, "CHARACTER SET utf8mb4 COLLATE utf8mb4_bin") {
				t.Errorf("%s: %s.%s %s must be CHARACTER SET utf8mb4 COLLATE utf8mb4_bin", version, table, name, typ)
			}
		}
		if strings.HasSuffix(name, "_at_us") && typ != "BIGINT" {
			t.Errorf("%s: %s.%s must be BIGINT epoch micros, is %s", version, table, name, typ)
		}
		if (strings.HasSuffix(name, "_id") || strings.HasSuffix(name, "_uid")) && name != "storage_backend_id" &&
			name != "backend_upload_id" && typ != "CHAR(26)" {
			t.Errorf("%s: %s.%s must be a ULID CHAR(26), is %s", version, table, name, typ)
		}
		col := table + "." + name
		if want, ok := byteColumns[col]; ok {
			seenByteColumns[col] = true
			if typ != want {
				t.Errorf("%s: %s must be %s (compared byte for byte), is %s", version, col, want, typ)
			}
		} else if (strings.HasSuffix(name, "_name") && name != "display_name" || strings.HasSuffix(name, "path") ||
			strings.HasPrefix(name, "storage_prefix") && name != "storage_prefix_key" || strings.HasSuffix(name, "storage_key")) &&
			!strings.HasPrefix(typ, "VARBINARY") {
			t.Errorf("%s: %s looks like a name, path or key: make it VARBINARY and list it in byteColumns", version, col)
		}
	}
}

func stripComments(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "--") {
			continue
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
