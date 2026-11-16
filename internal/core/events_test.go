package core_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LUOSENGWA/MFGraph/internal/authz"
	"github.com/LUOSENGWA/MFGraph/internal/core"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"github.com/LUOSENGWA/MFGraph/internal/ports/memory"
	"github.com/LUOSENGWA/MFGraph/internal/treestore"
)

// descendingIDs returns ULIDs that sort before every earlier one, so that
// the order of the outbox cannot come from the IDs.
type descendingIDs struct {
	mu sync.Mutex
	n  int
}

func (d *descendingIDs) NewID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.n++
	return fmt.Sprintf("01JD3X5Q8Z9W2V4T6R8P%06d", 999999-d.n)
}

type event struct {
	V          int    `json:"v"`
	EventID    string `json:"event_id"`
	OccurredAt string `json:"occurred_at"`
	ProjectID  string `json:"project_id"`
	Actor      struct {
		Type *string `json:"type"`
		ID   string  `json:"id"`
	} `json:"actor"`
	Data map[string]any `json:"data"`
}

func decodeEvents(t *testing.T, evs []ports.Event) []event {
	t.Helper()
	out := make([]event, len(evs))
	for i, ev := range evs {
		dec := json.NewDecoder(strings.NewReader(string(ev.Payload)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&out[i]); err != nil {
			t.Fatalf("envelope %s: %v", ev.Payload, err)
		}
	}
	return out
}

// wantEventID is the documented derivation (docs/events.md).
func wantEventID(kind, project, key string, version int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("mfgraph\n%s\n%s\n%s\n%d", kind, project, key, version)))
	return hex.EncodeToString(sum[:])
}

func keys(m map[string]any) string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, ",")
}

var occurredRe = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{6}Z$`)

// TestEvents checks every event of a workspace's life in module mode: the
// kinds, the envelope, the derived event IDs, the data fields, the reason
// of branch_updated, and the order of the outbox, which follows the order
// of emission even when the IDs sort the other way.
func TestEvents(t *testing.T) {
	store := memory.NewStore()
	objs := memory.NewBlobStore()
	trees, err := treestore.New(objs, func(ctx context.Context, ws string) (string, error) {
		w, err := store.GetWorkspace(ctx, nil, ws)
		return w.StoragePrefix, err
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := memory.NewClock(time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC))
	svc, err := core.New(core.Config{TenantID: moduleTenant, HostOwnsProjects: true}, core.Deps{Store: store, Objects: objs,
		Trees: trees, Authz: authz.ProjectScope{}, Clock: clock, IDs: &descendingIDs{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := svc.CreateWorkspace(ctx, workerP, core.CreateWorkspaceRequest{ProjectID: projP}, "")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := svc.PutObject(ctx, workerP, core.PutObjectRequest{WorkspaceID: ws.Workspace.ID, Body: strings.NewReader("x"), ContentLength: -1})
	if err != nil {
		t.Fatal(err)
	}
	c1, err := svc.Commit(ctx, managerP, ws.Workspace.ID, core.CommitRequest{BranchID: ws.MainBranchID, ExpectedGeneration: 1,
		Changes: []core.Change{put("x.txt", b.Hash.String())}}, "k1")
	if err != nil {
		t.Fatal(err)
	}
	r1, err := svc.Restore(ctx, workerP, c1.SnapshotID, core.RestoreRequest{BranchID: ws.MainBranchID, ExpectedGeneration: 2,
		Path: strPtr("x.txt")}, "")
	if err == nil || core.CodeOf(err) != core.CodeInvalidRequest {
		t.Fatalf("restoring what the head has: %+v %v", r1, err) // no_changes: nothing is written
	}
	c2, err := svc.Commit(ctx, managerP, ws.Workspace.ID, core.CommitRequest{BranchID: ws.MainBranchID, ExpectedGeneration: 2,
		Changes: []core.Change{del("x.txt")}}, "")
	if err != nil {
		t.Fatal(err)
	}
	r1, err = svc.Restore(ctx, workerP, c1.SnapshotID, core.RestoreRequest{BranchID: ws.MainBranchID, ExpectedGeneration: 3}, "")
	if err != nil {
		t.Fatal(err)
	}
	// A failing commit (stale generation) and an idempotent replay add
	// nothing.
	if _, err := svc.Commit(ctx, managerP, ws.Workspace.ID, core.CommitRequest{BranchID: ws.MainBranchID, ExpectedGeneration: 2,
		Changes: []core.Change{del("x.txt")}}, ""); core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("stale commit: %v", err)
	}
	if again, err := svc.Commit(ctx, managerP, ws.Workspace.ID, core.CommitRequest{BranchID: ws.MainBranchID, ExpectedGeneration: 1,
		Changes: []core.Change{put("x.txt", b.Hash.String())}}, "k1"); err != nil || again.SnapshotID != c1.SnapshotID {
		t.Fatalf("replay: %+v %v", again, err)
	}

	rows, _ := store.Events(ctx)
	// The forwarder reads the outbox by (created_at_us, event_id).
	sorted := append([]ports.Event(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].CreatedAtUs != sorted[j].CreatedAtUs {
			return sorted[i].CreatedAtUs < sorted[j].CreatedAtUs
		}
		return sorted[i].ID < sorted[j].ID
	})
	var kinds []string
	for i, ev := range sorted {
		kinds = append(kinds, ev.Type)
		if ev.ID != rows[i].ID {
			t.Fatalf("outbox order differs from the order of emission at %d: %s, want %s", i, ev.Type, rows[i].Type)
		}
	}
	want := []string{ports.EventWorkspaceCreated, ports.EventBranchCreated, ports.EventCommitCreated, ports.EventBranchUpdated,
		ports.EventCommitCreated, ports.EventBranchUpdated, ports.EventSnapshotRestored, ports.EventBranchUpdated}
	if strings.Join(kinds, " ") != strings.Join(want, " ") {
		t.Fatalf("events:\n%v\nwant\n%v", kinds, want)
	}
	evs := decodeEvents(t, sorted)
	main, wsID := ws.MainBranchID, ws.Workspace.ID
	type check struct {
		actor, actorType string
		key              string
		version          int64
		fields           string
	}
	snapFields := "branch_id,entry_count,parent_ids,ref_name,root_tree_hash,snapshot_id,total_bytes,trigger_kind,workspace_id"
	branchFields := "branch_id,generation,head_snapshot_id,previous_head_snapshot_id,reason,ref_name,workspace_id"
	checks := []check{
		{"worker-7", "worker", wsID, 1, "main_branch_id,storage_backend_id,workspace_id"},
		{"worker-7", "worker", main, 1, "branch_id,generation,ref_name,workspace_id"},
		{"manager", "human", c1.SnapshotID, 1, snapFields},
		{"manager", "human", main, 2, branchFields},
		{"manager", "human", c2.SnapshotID, 1, snapFields},
		{"manager", "human", main, 3, branchFields},
		{"worker-7", "worker", r1.SnapshotID, 1, "on_behalf_of,restored_from," + snapFields},
		{"worker-7", "worker", main, 4, branchFields},
	}
	for i, c := range checks {
		ev := evs[i]
		if ev.V != 1 || ev.ProjectID != projP || ev.Actor.ID != c.actor || ev.Actor.Type == nil || *ev.Actor.Type != c.actorType ||
			!occurredRe.MatchString(ev.OccurredAt) || ev.EventID != wantEventID(kinds[i], projP, c.key, c.version) ||
			sorted[i].AggregateID != c.key || sorted[i].WorkspaceID != wsID {
			t.Errorf("%s: envelope %+v (row %+v)", kinds[i], ev, sorted[i])
		}
		if got := keys(ev.Data); got != strings.Join(sortedList(c.fields), ",") {
			t.Errorf("%s: data fields %s, want %s", kinds[i], got, c.fields)
		}
		if strings.Contains(string(sorted[i].Payload), "tenants/") {
			t.Errorf("%s: the payload names a storage location: %s", kinds[i], sorted[i].Payload)
		}
	}
	// Events of one transaction: one microsecond apart, the same time.
	if sorted[1].CreatedAtUs != sorted[0].CreatedAtUs+1 || evs[1].OccurredAt != evs[0].OccurredAt {
		t.Errorf("times of one transaction: %d %d, %s %s", sorted[0].CreatedAtUs, sorted[1].CreatedAtUs, evs[0].OccurredAt, evs[1].OccurredAt)
	}
	for i, reason := range map[int]string{3: "commit", 5: "commit", 7: "restore"} {
		if evs[i].Data["reason"] != reason {
			t.Errorf("event %d: reason %v, want %s", i, evs[i].Data["reason"], reason)
		}
	}
	if evs[3].Data["previous_head_snapshot_id"] != nil || evs[5].Data["previous_head_snapshot_id"] != c1.SnapshotID ||
		evs[7].Data["head_snapshot_id"] != r1.SnapshotID || evs[6].Data["restored_from"] != c1.SnapshotID ||
		evs[2].Data["trigger_kind"] != "manual" || evs[6].Data["trigger_kind"] != "restore" {
		t.Errorf("branch heads: %+v %+v %+v", evs[3].Data, evs[5].Data, evs[7].Data)
	}
	if obo, ok := evs[6].Data["on_behalf_of"].(map[string]any); !ok || obo["id"] != "manager" || obo["type"] != "human" {
		t.Errorf("on_behalf_of: %v", evs[6].Data["on_behalf_of"])
	}
	if evs[1].Data["ref_name"] != "main" || evs[1].Data["generation"] != float64(1) || evs[0].Data["main_branch_id"] != main {
		t.Errorf("creation events: %v %v", evs[0].Data, evs[1].Data)
	}
	// Every event has its own ID, and IDs are stable: deriving them again
	// gives the same values.
	seen := map[string]bool{}
	for _, ev := range evs {
		if seen[ev.EventID] {
			t.Errorf("duplicate event_id %s", ev.EventID)
		}
		seen[ev.EventID] = true
	}
	// Standalone deployments: the same kinds, actor type null.
	e := newEnv(t, core.Config{})
	e.seed(alice)
	rows, _ = e.store.Events(ctx)
	sa := decodeEvents(t, rows)
	if len(sa) != 4 || sa[0].Actor.Type != nil || sa[0].Actor.ID != "alice" || rows[0].Type != ports.EventWorkspaceCreated {
		t.Fatalf("standalone events: %+v", sa)
	}
}

func sortedList(s string) []string {
	l := strings.Split(s, ",")
	sort.Strings(l)
	return l
}

func strPtr(s string) *string { return &s }
