// Package extref validates external references: identifiers that a host
// platform generates in a format MFGraph does not control, such as the
// workflow instance ID recorded with a snapshot. Actor references
// (internal/actorref) are external references too.
//
// An external reference is:
//
//   - non-empty valid UTF-8,
//   - at most MaxBytes (255) bytes,
//   - free of control characters (U+0000-U+001F, U+007F-U+009F).
//
// It is stored exactly as given (VARBINARY(255)) and compared byte for byte:
// no Unicode normalization, no case folding, no check that it is a ULID.
// IDs that MFGraph generates itself, and the project IDs a host platform
// passes (verified to be ULIDs), are CHAR(26) ULIDs instead.
package extref

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

// MaxBytes is the maximum length of an external reference, in bytes.
const MaxBytes = 255

// ErrInvalid is matched (errors.Is) by every error of Validate.
var ErrInvalid = errors.New("invalid external reference")

// Problem returns which rule s breaks ("" when s is a valid external
// reference). The text never repeats s.
func Problem(s string) string {
	switch {
	case s == "":
		return "empty"
	case len(s) > MaxBytes:
		return fmt.Sprintf("%d bytes, the limit is %d", len(s), MaxBytes)
	case !utf8.ValidString(s):
		return "not valid UTF-8"
	}
	for _, r := range s {
		if r < 0x20 || 0x7f <= r && r <= 0x9f {
			return fmt.Sprintf("contains the control character %U", r)
		}
	}
	return ""
}

// Validate reports whether s is an external reference.
func Validate(s string) error {
	if p := Problem(s); p != "" {
		return fmt.Errorf("%w: %s", ErrInvalid, p)
	}
	return nil
}

// Valid reports whether Validate(s) succeeds.
func Valid(s string) bool { return Problem(s) == "" }
