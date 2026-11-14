package storagekey

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
)

const (
	// MaxKeyBytes is the longest full object key: the key length limit of
	// Amazon S3 (1024 bytes), so that every backend can hold every key.
	MaxKeyBytes = 1024

	// longestRelativeKey is the length of "/" + TreeKey(h), the longest key
	// below a prefix:
	//
	//	"/tree/" + <hh> + "/" + <64 hex digits> + ".json.zst"
	//	   6    +  2   +  1  +       64        +      9      = 82 bytes
	//
	// ("/" + ObjectKey(h) is "/objects/" + <hh> + "/" + <64 hex> = 76 bytes.)
	longestRelativeKey = len("/tree/") + 2 + len("/") + 64 + len(".json.zst")

	// MaxPrefixBytes bounds a storage prefix: 1024 - 82 = 942 bytes, so that
	// prefix + "/" + TreeKey(h) (and the shorter content key) never exceeds
	// MaxKeyBytes. The workspaces.storage_prefix column is VARBINARY(942).
	MaxPrefixBytes = MaxKeyBytes - longestRelativeKey
)

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
// Each ID must be a canonical ULID (26 uppercase Crockford base32
// characters). Because the IDs have a fixed length and every default prefix
// has the same six segments, default prefixes of different workspaces are
// never equal, not even ignoring case, and never nested.
func DefaultPrefix(tenantID, projectID, workspaceID string) (string, error) {
	for _, id := range []string{tenantID, projectID, workspaceID} {
		if !isULID(id) {
			return "", fmt.Errorf("%w: ID %q is not a canonical ULID", ErrInvalidPrefix, id)
		}
	}
	p := "tenants/" + tenantID + "/projects/" + projectID + "/workspaces/" + workspaceID
	if err := ValidatePrefix(p); err != nil {
		return "", err
	}
	return p, nil
}

// isULID reports a canonical ULID: 26 characters of the uppercase Crockford
// base32 alphabet, the first one 0-7. (internal/ids has the same check; this
// package keeps to the standard library.)
func isULID(s string) bool {
	if len(s) != 26 || s[0] > '7' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", rune(s[i])) {
			return false
		}
	}
	return true
}

// ValidatePrefix checks a storage prefix: non-empty, at most MaxPrefixBytes
// bytes, no leading or trailing '/', and '/'-separated segments that are
// non-empty, consist only of ASCII letters, digits, '.', '_' and '-', and are
// not "." or "..". Prefixes are immutable once a workspace exists. Whether a
// prefix conflicts with the prefix of another workspace is decided by
// PrefixesConflict.
func ValidatePrefix(p string) error {
	if p == "" {
		return fmt.Errorf("%w: empty", ErrInvalidPrefix)
	}
	if len(p) > MaxPrefixBytes {
		return fmt.Errorf("%w: %d bytes exceed %d", ErrInvalidPrefix, len(p), MaxPrefixBytes)
	}
	if p[0] == '/' || p[len(p)-1] == '/' {
		return fmt.Errorf("%w: %q has a leading or trailing '/'", ErrInvalidPrefix, p)
	}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "":
			return fmt.Errorf("%w: %q has an empty segment", ErrInvalidPrefix, p)
		case ".", "..":
			return fmt.Errorf("%w: %q has a %q segment", ErrInvalidPrefix, p, seg)
		}
		for i := 0; i < len(seg); i++ {
			if !isPrefixChar(seg[i]) {
				return fmt.Errorf("%w: %q contains %q; segments use only A-Z a-z 0-9 . _ -", ErrInvalidPrefix, p, seg[i])
			}
		}
	}
	return nil
}

func isPrefixChar(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '.' || c == '_' || c == '-'
}

// asciiLower maps A-Z to a-z and leaves every other byte unchanged.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// PrefixesConflict reports whether two storage prefixes cannot both be used
// on the same storage backend: compared ASCII case-insensitively, they are
// equal, or one is a '/'-separated ancestor of the other ("a" and "a/b"
// conflict, "a" and "ab" do not). Garbage collection lists a workspace's
// objects by prefix, and some backends do not distinguish case, so either
// situation would let one workspace see (and delete) another's objects. Both
// arguments are expected to satisfy ValidatePrefix. The function is pure;
// the database enforces equality through storage_prefix_key, and creating a
// workspace must also check nesting.
func PrefixesConflict(a, b string) bool {
	a, b = asciiLower(a), asciiLower(b)
	if len(a) > len(b) {
		a, b = b, a
	}
	return a == b || strings.HasPrefix(b, a) && b[len(a)] == '/'
}

// PrefixKey returns the value of the workspaces.storage_prefix_key column:
// the lowercase hex SHA-256 of the ASCII-lowercased prefix. Two prefixes that
// differ only in ASCII case have the same key, so a unique index on
// (storage_backend_id, storage_prefix_key) rejects them.
func PrefixKey(p string) string {
	sum := sha256.Sum256([]byte(asciiLower(p)))
	return hex.EncodeToString(sum[:])
}

// Join returns the full object key prefix + "/" + key. The database stores
// only the relative key.
func Join(prefix, key string) string { return prefix + "/" + key }
