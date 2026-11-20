package mysql_test

import (
	"context"
	"sync"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/store/mysql"
)

func TestMySQLBackfillBlobUsage(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	s := mysql.New(db)
	add := func(ws, data string) {
		t.Helper()
		h := contenthash.SumBytes([]byte(data))
		err := s.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
			return s.InsertBlob(ctx, tx, ports.Blob{WorkspaceID: ws, Hash: h, Size: int64(len(data)), StorageKey: h.Hex(), Status: ports.BlobStaged, CreatedAtUs: 1})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, ws := range []string{"alpha", "beta"} {
		if err := s.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error { return s.InsertUsage(ctx, tx, ws, 1) }); err != nil {
			t.Fatal(err)
		}
		add(ws, "abc")
		add(ws, "four")
	}
	// A pre-upgrade partial tally is replaced by the verified blob total.
	// The live reservation must survive throughout the backfill.
	if _, err := db.Exec("UPDATE workspace_usage SET used_bytes=2,reserved_bytes=11"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.BackfillBlobUsage(ctx, 10, 1); err != nil || n != 1 {
		t.Fatalf("first bounded batch: %d %v", n, err)
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			for {
				n, err := s.BackfillBlobUsage(ctx, 11, 2)
				if err != nil {
					t.Error(err)
					return
				}
				if n == 0 {
					return
				}
			}
		})
	}
	wg.Wait()
	add("alpha", "later")
	for _, want := range []int{1, 0} {
		if n, err := s.BackfillBlobUsage(ctx, 12, 8); err != nil || n != want {
			t.Fatalf("late older writer: %d %v, want %d", n, err, want)
		}
	}
	for ws, used := range map[string]int64{"alpha": 12, "beta": 7} {
		u, err := s.GetUsage(ctx, nil, ws)
		if err != nil || u.Used != used || u.Reserved != 11 {
			t.Fatalf("workspace %s: usage=%+v err=%v", ws, u, err)
		}
	}
}
