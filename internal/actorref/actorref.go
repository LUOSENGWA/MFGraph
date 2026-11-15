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
// "a " are two different actors.
package actorref

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

// MaxBytes is the maximum length of an actor reference, in bytes.
const MaxBytes = 255

// ErrInvalid is matched (errors.Is) by every validation error.
var ErrInvalid = errors.New("invalid actor reference")

// Validate reports whether ref is an actor reference (see the package
// documentation). The error says which rule failed; it never repeats ref.
func Validate(ref string) error {
	switch {
	case ref == "":
		return fmt.Errorf("%w: empty", ErrInvalid)
	case len(ref) > MaxBytes:
		return fmt.Errorf("%w: %d bytes, the limit is %d", ErrInvalid, len(ref), MaxBytes)
	case !utf8.ValidString(ref):
		return fmt.Errorf("%w: not valid UTF-8", ErrInvalid)
	}
	for _, r := range ref {
		if r < 0x20 || 0x7f <= r && r <= 0x9f {
			return fmt.Errorf("%w: contains the control character %U", ErrInvalid, r)
		}
	}
	return nil
}

// Valid reports whether Validate(ref) succeeds.
func Valid(ref string) bool { return Validate(ref) == nil }
