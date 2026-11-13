package storagekey

import (
	"errors"
	"fmt"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/pathx"
)

// MaxPrefixBytes bounds a storage prefix so that it fits the
// workspaces.storage_prefix column (VARCHAR(1024)).
const MaxPrefixBytes = 1024

// ErrInvalidPrefix is returned (wrapped) for an unusable storage prefix.
var ErrInvalidPrefix = errors.New("storagekey: invalid storage prefix")

func mustHex(h contenthash.Hash) string {
	if h.IsZero() {
		panic("storagekey: zero hash")
	}
	return h.Hex()
}

// ObjectKey returns the key of a whole-file content object, relative to the
// workspace storage prefix: objects/<hh>/<hex>, where <hex> is the 64-digit
// lowercase SHA-256 and <hh> its first two digits. It panics on the zero hash.
func ObjectKey(h contenthash.Hash) string {
	x := mustHex(h)
	return "objects/" + x[:2] + "/" + x
}

// TreeKey returns the key of a tree node, relative to the workspace storage
// prefix: tree/<hh>/<hex>.json.zst. The hash is that of the uncompressed
// canonical bytes; the stored object is zstd-compressed. It panics on the
// zero hash.
func TreeKey(h contenthash.Hash) string {
	x := mustHex(h)
	return "tree/" + x[:2] + "/" + x + ".json.zst"
}

// DefaultPrefix returns the default storage prefix of a project-level
// workspace: tenants/<tenant_id>/projects/<project_id>/workspaces/<workspace_id>.
// Each ID must be a single valid path segment.
func DefaultPrefix(tenantID, projectID, workspaceID string) (string, error) {
	for _, id := range []string{tenantID, projectID, workspaceID} {
		if n, err := pathx.NormalizeSegment(id); err != nil || n != id {
			return "", fmt.Errorf("%w: ID %q is not a single normalized path segment", ErrInvalidPrefix, id)
		}
	}
	p := "tenants/" + tenantID + "/projects/" + projectID + "/workspaces/" + workspaceID
	if err := ValidatePrefix(p); err != nil {
		return "", err
	}
	return p, nil
}

// ValidatePrefix checks a storage prefix: non-empty, no leading or trailing
// '/', every segment valid under the path rules, already in NFC, and at most
// MaxPrefixBytes bytes. Prefixes are immutable once a workspace exists.
func ValidatePrefix(p string) error {
	if p == "" {
		return fmt.Errorf("%w: empty", ErrInvalidPrefix)
	}
	if p[0] == '/' || p[len(p)-1] == '/' {
		return fmt.Errorf("%w: %q has a leading or trailing '/'", ErrInvalidPrefix, p)
	}
	if len(p) > MaxPrefixBytes {
		return fmt.Errorf("%w: %d bytes exceed %d", ErrInvalidPrefix, len(p), MaxPrefixBytes)
	}
	n, err := pathx.Normalize(p)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPrefix, err)
	}
	if n != p {
		return fmt.Errorf("%w: %q is not in NFC", ErrInvalidPrefix, p)
	}
	return nil
}

// Join returns the full object key prefix + "/" + key. The database stores
// only the relative key.
func Join(prefix, key string) string { return prefix + "/" + key }
