// Package actorref validates actor references: the string that names who
// performs an operation (the authenticated caller, or the one it acts on
// behalf of).
//
// A host platform passes its own identity strings, for example a worker
// name, a user name such as "manager" or an ID such as "@alice:example.com";
// in a standalone deployment the actor reference of a principal is the
// string form of its principal_uid (a ULID). An actor reference is:
//
//   - non-empty valid UTF-8,
//   - at most MaxBytes (255) bytes,
//   - free of control characters (U+0000-U+001F, U+007F-U+009F).
//
// It is stored exactly as given and compared byte for byte: no Unicode
// normalization, no case folding, and no check that it is a ULID. "a" and
// "a " are two different actors. These are the rules of every external
// reference (internal/extref); this package applies them to actors.
package actorref

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/LUOSENGWA/MFGraph/internal/extref"
)

// MaxBytes is the maximum length of an actor reference, in bytes.
const MaxBytes = extref.MaxBytes

// ErrInvalid is matched (errors.Is) by every validation error.
var ErrInvalid = errors.New("invalid actor reference")

// Validate reports whether ref is an actor reference (see the package
// documentation). The error says which rule failed; it never repeats ref.
func Validate(ref string) error {
	if p := extref.Problem(ref); p != "" {
		return fmt.Errorf("%w: %s", ErrInvalid, p)
	}
	return nil
}

// Valid reports whether Validate(ref) succeeds.
func Valid(ref string) bool { return extref.Valid(ref) }

// Pseudonymizer derives the value logged for an actor: the first 16 hex
// digits of HMAC-SHA256(key, ref), with a key of the deployment. The log
// records of one actor stay linkable while the key is the same, but they
// do not show who it is, and without the key the pseudonym of a known
// reference (a user or worker name) cannot be computed to look it up.
// Records written with different keys cannot be linked.
//
// Logging rule: actor references are personal data (a user name, an
// @user:server ID). Log records at info level and above carry only
// "actor" = Pseudonym(ref), never the reference itself or a principal UID
// (which, in a standalone deployment, is the reference). The raw values may
// appear only in debug-level records.
type Pseudonymizer struct{ key []byte }

// NewPseudonymizer returns a Pseudonymizer with key (it must not be empty).
func NewPseudonymizer(key []byte) (*Pseudonymizer, error) {
	if len(key) == 0 {
		return nil, errors.New("actorref: empty pseudonym key")
	}
	return &Pseudonymizer{key: append([]byte(nil), key...)}, nil
}

// Pseudonym returns the pseudonym of ref.
func (p *Pseudonymizer) Pseudonym(ref string) string {
	m := hmac.New(sha256.New, p.key)
	m.Write([]byte(ref))
	return hex.EncodeToString(m.Sum(nil)[:8])
}
