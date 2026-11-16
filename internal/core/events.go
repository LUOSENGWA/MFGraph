package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

// Events (docs/events.md). A change writes its events to the outbox
// (outbox_events) in its own transaction, so an event exists exactly when
// the change was committed; module mode forwards them to the host
// platform's outbox afterwards. Every event is an envelope:
//
//	{"v":1,"event_id":"<64 hex>","occurred_at":"<RFC 3339 UTC, microseconds>",
//	 "project_id":"...","actor":{"type":"<type or null>","id":"<actor reference>"},
//	 "data":{...}}
//
// event_id is derived from the content (eventID), so the same event always
// has the same ID and consumers deduplicate by it. Events carry IDs, refs,
// hashes, counts and actor references; never file contents, file paths,
// tokens or storage locations.

// EventVersion is the version of the envelope ("v").
const EventVersion = 1

// MaxEventBytes bounds an envelope. A larger one is refused, never
// truncated: the change fails with an internal error and is rolled back.
// Real envelopes have a few KiB at most.
const MaxEventBytes = 256 << 10

// errEventTooLarge: an envelope larger than MaxEventBytes.
var errEventTooLarge = errors.New("core: event larger than 256 KiB")

// Reasons of mfgraph.branch_updated (data.reason).
const (
	ReasonCommit  = "commit"
	ReasonRestore = "restore"
)

// eventActor is an actor of an envelope: the type is null when unknown (a
// standalone deployment has no actor types).
type eventActor struct {
	Type *string `json:"type"`
	ID   string  `json:"id"`
}

func actorOf(ref, typ string) eventActor {
	a := eventActor{ID: ref}
	if typ != "" {
		a.Type = &typ
	}
	return a
}

// envelope is the payload of every outbox event.
type envelope struct {
	V          int        `json:"v"`
	EventID    string     `json:"event_id"`
	OccurredAt string     `json:"occurred_at"`
	ProjectID  string     `json:"project_id"`
	Actor      eventActor `json:"actor"`
	Data       any        `json:"data"`
}

// commitData is the data of mfgraph.commit_created. on_behalf_of names the
// actor the author acted for, when there is one.
type commitData struct {
	WorkspaceID  string      `json:"workspace_id"`
	SnapshotID   string      `json:"snapshot_id"`
	BranchID     string      `json:"branch_id"`
	RefName      string      `json:"ref_name"`
	RootTreeHash string      `json:"root_tree_hash"`
	ParentIDs    []string    `json:"parent_ids"`
	TriggerKind  string      `json:"trigger_kind"`
	EntryCount   int64       `json:"entry_count"`
	TotalBytes   int64       `json:"total_bytes"`
	OnBehalfOf   *eventActor `json:"on_behalf_of,omitempty"`
}

// restoreData is the data of mfgraph.snapshot_restored: that of a commit
// and the snapshot that was restored.
type restoreData struct {
	commitData
	RestoredFrom string `json:"restored_from"`
}

// branchUpdatedData is the data of mfgraph.branch_updated: the head moved
// (reason commit or restore).
type branchUpdatedData struct {
	WorkspaceID            string  `json:"workspace_id"`
	BranchID               string  `json:"branch_id"`
	RefName                string  `json:"ref_name"`
	HeadSnapshotID         string  `json:"head_snapshot_id"`
	PreviousHeadSnapshotID *string `json:"previous_head_snapshot_id"`
	Generation             int64   `json:"generation"`
	Reason                 string  `json:"reason"`
}

// workspaceCreatedData is the data of mfgraph.workspace_created (never the
// storage prefix).
type workspaceCreatedData struct {
	WorkspaceID      string `json:"workspace_id"`
	MainBranchID     string `json:"main_branch_id"`
	StorageBackendID string `json:"storage_backend_id"`
}

// branchCreatedData is the data of mfgraph.branch_created.
type branchCreatedData struct {
	WorkspaceID string `json:"workspace_id"`
	BranchID    string `json:"branch_id"`
	RefName     string `json:"ref_name"`
	Generation  int64  `json:"generation"`
}

// eventData lists the data of every event this version emits
// (mfgraph.snapshot_created is registered but not emitted).
var eventData = map[string]any{
	ports.EventWorkspaceCreated: workspaceCreatedData{},
	ports.EventBranchCreated:    branchCreatedData{},
	ports.EventCommitCreated:    commitData{},
	ports.EventSnapshotRestored: restoreData{},
	ports.EventBranchUpdated:    branchUpdatedData{},
	ports.EventSnapshotCreated:  nil,
}

// eventID derives the ID of an event from its content:
//
//	hex(sha256("mfgraph\n" + kind + "\n" + scope_id + "\n" + key + "\n" + version))
//
// scope_id is the project ID; key is the snapshot ID (commit_created,
// snapshot_restored), the branch ID (branch_created, branch_updated) or the
// workspace ID (workspace_created); version is the new generation of the
// branch for branch events and 1 otherwise, in decimal.
func eventID(kind, projectID, key string, version int64) string {
	sum := sha256.Sum256([]byte("mfgraph\n" + kind + "\n" + projectID + "\n" + key + "\n" + strconv.FormatInt(version, 10)))
	return hex.EncodeToString(sum[:])
}

// eventSpec is one event of a change.
type eventSpec struct {
	kind        string
	workspaceID string
	key         string // the business key of eventID, also the aggregate ID of the outbox row
	version     int64
	data        any
}

// events builds the outbox rows of the events of one change, in the order
// they are emitted: the n-th event gets the time occurredUs + n as its
// outbox time (created_at_us), so that ordering the outbox by
// (created_at_us, event_id) keeps the order of a transaction whatever IDs
// the ID generator returns. occurred_at is occurredUs for all of them.
func (s *Service) events(p ports.Principal, projectID string, occurredUs int64, specs ...eventSpec) ([]ports.Event, error) {
	out := make([]ports.Event, 0, len(specs))
	for i, e := range specs {
		payload, err := marshalEvent(envelope{V: EventVersion, EventID: eventID(e.kind, projectID, e.key, e.version),
			OccurredAt: rfc3339(occurredUs), ProjectID: projectID, Actor: actorOf(p.Ref, p.ActorType), Data: e.data})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.kind, err)
		}
		out = append(out, ports.Event{ID: s.d.IDs.NewID(), Type: e.kind, WorkspaceID: e.workspaceID, AggregateID: e.key,
			Payload: payload, CreatedAtUs: occurredUs + int64(i)})
	}
	return out, nil
}

// marshalEvent encodes an envelope and refuses one above MaxEventBytes.
func marshalEvent(env envelope) ([]byte, error) {
	b, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxEventBytes {
		return nil, fmt.Errorf("%w (%d bytes)", errEventTooLarge, len(b))
	}
	return b, nil
}

// appendEvents appends events in the transaction of the change.
func (s *Service) appendEvents(ctx context.Context, tx ports.Tx, evs []ports.Event) error {
	for _, ev := range evs {
		if err := s.d.Store.Append(ctx, tx, ev); err != nil {
			return err
		}
	}
	return nil
}
