package core

// Payloads of the outbox events (outbox_events.payload_json). The first
// core slice writes them in the transaction of the change and does not
// publish them yet. They carry IDs, refs, hashes, counts and the actor
// reference; never file contents, file paths, tokens or storage locations.
// schema_version is incremented for incompatible changes.

// snapshotEvent is the payload of snapshot.created (a commit) and
// snapshot.restored (a restore, with restored_from).
type snapshotEvent struct {
	SchemaVersion int      `json:"schema_version"`
	TenantID      string   `json:"tenant_id"`
	ProjectID     string   `json:"project_id"`
	WorkspaceID   string   `json:"workspace_id"`
	SnapshotID    string   `json:"snapshot_id"`
	BranchID      string   `json:"branch_id"`
	RefName       string   `json:"ref_name"`
	RootTreeHash  string   `json:"root_tree_hash"`
	ParentIDs     []string `json:"parent_ids"`
	TriggerKind   string   `json:"trigger_kind"`
	RestoredFrom  string   `json:"restored_from,omitempty"`
	ActorRef      string   `json:"actor_ref"`
	EntryCount    int64    `json:"entry_count"`
	TotalBytes    int64    `json:"total_bytes"`
	CreatedAt     string   `json:"created_at"`
}

// branchUpdatedEvent is the payload of branch.updated: the head moved.
type branchUpdatedEvent struct {
	SchemaVersion          int     `json:"schema_version"`
	TenantID               string  `json:"tenant_id"`
	ProjectID              string  `json:"project_id"`
	WorkspaceID            string  `json:"workspace_id"`
	BranchID               string  `json:"branch_id"`
	RefName                string  `json:"ref_name"`
	HeadSnapshotID         string  `json:"head_snapshot_id"`
	PreviousHeadSnapshotID *string `json:"previous_head_snapshot_id"`
	Generation             int64   `json:"generation"`
	ActorRef               string  `json:"actor_ref"`
	UpdatedAt              string  `json:"updated_at"`
}
