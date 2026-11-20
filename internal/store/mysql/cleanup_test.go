package mysql_test

import (
	"context"
	"github.com/LUOSENGWA/MFGraph/internal/store/mysql"
	"testing"
)

func TestBoundedMetadataRetention(t *testing.T) {
	db := openTestDB(t)
	for _, x := range []struct {
		id, status, kind string
		published        int
	}{{"old", "published", "mfgraph.commit_created", 10}, {"new", "published", "mfgraph.commit_created", 30}, {"pending", "pending", "mfgraph.commit_created", 0}, {"skipped", "skipped", "mfgraph.commit_created", 0}, {"foreign", "published", "other.kind", 10}} {
		if _, err := db.Exec(`INSERT INTO outbox_events(event_id,event_type,payload_json,status,retry_count,created_at_us,published_at_us) VALUES(?,?,'{}',?,0,1,?)`, x.id, x.kind, x.status, x.published); err != nil {
			t.Fatal(err)
		}
	}
	for _, x := range []struct {
		key     string
		expires int
	}{{"oldA", 9}, {"oldB", 10}, {"live", 11}} {
		if _, err := db.Exec(`INSERT INTO idempotency_keys(idempotency_key,actor_ref,route,request_hash,status,created_at_us,expires_at_us) VALUES(?,'a','r','hash','completed',1,?)`, x.key, x.expires); err != nil {
			t.Fatal(err)
		}
	}
	s := mysql.New(db)
	ctx := context.Background()
	for _, tc := range []struct{ cutoff, idem, events int64 }{{0, 1, 0}, {20, 1, 1}, {20, 0, 0}} {
		i, e, err := s.PruneMetadata(ctx, 10, tc.cutoff, 1)
		if err != nil || i != tc.idem || e != tc.events {
			t.Fatalf("retention %d: %d/%d %v", tc.cutoff, i, e, err)
		}
	}
	var remaining int
	if err := db.QueryRow("SELECT COUNT(*) FROM outbox_events WHERE event_id IN ('new','pending','skipped','foreign')").Scan(&remaining); err != nil || remaining != 4 {
		t.Fatalf("retained %d %v", remaining, err)
	}
}
