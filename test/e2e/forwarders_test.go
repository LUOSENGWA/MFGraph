//go:build e2e

package e2e

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestTwoModuleForwarders(t *testing.T) {
	m := newModuleEnv(t)
	m.migrate()
	// Both module processes share the module database, object directory
	// and host sink. Give each process its own private listening socket.
	twin := *m
	twin.sock = filepath.Join(filepath.Dir(m.sock), "second.sock")
	if _, err := m.ops.Exec("RENAME TABLE module_outbox TO paused_sink"); err != nil {
		t.Fatal(err)
	}
	a, b := m.startAnswering(nil), twin.startAnswering(nil)
	claims := defaultClaims(projP)
	var servers = []*moduleServer{a, b}
	for i := range 8 {
		servers[i%2].workspace(claims)
	}
	if _, err := m.ops.Exec("RENAME TABLE paused_sink TO module_outbox"); err != nil {
		t.Fatal(err)
	}
	got := m.waitHostRows("", 16, 20*time.Second)
	// The sink auto-increment order must match the real commit order even
	// when the second process observes and retries the same pending batch.
	compare := func(rows *sql.Rows, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			var payload []byte
			if err := rows.Scan(&payload); err != nil {
				t.Fatal(err)
			}
			var envelope struct {
				ID string `json:"event_id"`
			}
			if err := json.Unmarshal(payload, &envelope); err != nil {
				t.Fatal(err)
			}
			if n >= len(got) || got[n].eventID != envelope.ID {
				t.Fatalf("host event %d differs from committed order", n)
			}
			n++
		}
		if err := rows.Err(); err != nil || n != len(got) {
			t.Fatalf("read %d events: %v", n, err)
		}
	}
	compare(m.db.Query("SELECT payload_json FROM outbox_events ORDER BY commit_seq,created_at_us,event_id"))
	a.stop()
	b.stop()
	var pending int
	if err := m.db.QueryRow("SELECT COUNT(*) FROM outbox_events WHERE status='pending'").Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("%d undelivered events: %v", pending, err)
	}
}
