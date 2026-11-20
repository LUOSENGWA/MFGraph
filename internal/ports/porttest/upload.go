package porttest

import (
	"context"
	"errors"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"reflect"
	"testing"
)

func testUploadSessions(t *testing.T, s Store) {
	u := ports.Upload{ID: brA, WorkspaceID: wsA, Path: "sub/🎬.mp4", Size: 12, PartSize: 8, Total: 2, Backend: "default", Status: ports.UploadInitiated, Owner: "alice", Created: 1, Expires: 100}
	tx(t, s, func(tx ports.Tx) error {
		if err := s.InsertUsage(ctx, tx, wsA, 1); err != nil {
			return err
		}
		if err := s.InsertUpload(ctx, tx, u); err != nil {
			return err
		}
		return s.SetUsage(ctx, tx, wsA, ports.Usage{Reserved: 12}, 1)
	})
	got, err := s.GetUpload(ctx, nil, u.ID)
	if err != nil || got != u {
		t.Fatalf("session = %+v, %v", got, err)
	}
	rollback := errors.New("rollback")
	err = s.WithTx(ctx, func(ctx context.Context, tx ports.Tx) error {
		v, err := s.GetUpload(ctx, tx, u.ID)
		if err != nil {
			return err
		}
		v.Status = ports.UploadCompleted
		if err := s.UpdateUpload(ctx, tx, v); err != nil {
			return err
		}
		if err := s.SetUsage(ctx, tx, wsA, ports.Usage{Used: 12}, 2); err != nil {
			return err
		}
		return rollback
	})
	usage, readErr := s.GetUsage(ctx, nil, wsA)
	if !errors.Is(err, rollback) || readErr != nil || usage != (ports.Usage{Reserved: 12}) {
		t.Fatalf("rollback %v %+v %v", err, usage, readErr)
	}
	tx(t, s, func(tx ports.Tx) error {
		for _, p := range []ports.Part{{Number: 2, Size: 4, ETag: "tail"}, {Number: 1, Size: 8, ETag: "before"}, {Number: 1, Size: 8, ETag: "after"}} {
			if err := s.SaveUploadPart(ctx, tx, u.ID, p, 3); err != nil {
				return err
			}
		}
		return nil
	})
	parts, err := s.UploadParts(ctx, nil, u.ID)
	if err != nil || !reflect.DeepEqual(parts, []ports.Part{{Number: 1, Size: 8, ETag: "after"}, {Number: 2, Size: 4, ETag: "tail"}}) {
		t.Fatalf("parts = %+v %v", parts, err)
	}
	for _, tc := range []struct {
		now  int64
		want int
	}{{99, 0}, {100, 1}} {
		ids, err := s.PendingUploadCleanup(ctx, "default", tc.now, "", 8)
		if err != nil || len(ids) != tc.want {
			t.Fatalf("pending at %d: %v %v", tc.now, ids, err)
		}
	}
}
