package pathx

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	// MaxSegmentBytes is the maximum length of one path segment, in bytes of
	// its NFC UTF-8 form.
	MaxSegmentBytes = 255
	// MaxPathBytes is the maximum length of a whole path, in bytes of its NFC
	// UTF-8 form (including the '/' separators).
	MaxPathBytes = 4096
)

// Kind classifies why a path, segment or set of sibling names was rejected.
// The values are stable and are used in test vectors and API error details.
type Kind string

const (
	KindEmpty           Kind = "empty"             // empty input
	KindInvalidUTF8     Kind = "invalid_utf8"      // not valid UTF-8
	KindAbsolute        Kind = "absolute"          // starts with '/'
	KindBackslash       Kind = "backslash"         // contains '\'
	KindControlChar     Kind = "control_char"      // contains a control character: U+0000-U+001F, U+007F-U+009F
	KindDisallowedChar  Kind = "disallowed_char"   // contains a bidi control or an invisible zero-width character
	KindUnassignedChar  Kind = "unassigned_char"   // contains a code point unassigned in UnicodeVersion (write time only)
	KindEmptySegment    Kind = "empty_segment"     // "a//b" or trailing '/'
	KindDotSegment      Kind = "dot_segment"       // a segment equal to "." or ".."
	KindSegmentTooLong  Kind = "segment_too_long"  // segment > MaxSegmentBytes after NFC
	KindPathTooLong     Kind = "path_too_long"     // path > MaxPathBytes after NFC
	KindContainsSlash   Kind = "contains_slash"    // single-segment input contains '/'
	KindDuplicate       Kind = "duplicate"         // the same name (or path) twice
	KindCaseConflict    Kind = "case_conflict"     // distinct names with equal FoldKey
	KindFileDirConflict Kind = "file_dir_conflict" // same name used as file and directory
)

// ErrInvalidPath is matched (errors.Is) by every *Error.
var ErrInvalidPath = errors.New("invalid path")

// Error describes a rejected path, segment or sibling set.
type Error struct {
	Kind   Kind
	Input  string // the offending path, segment or name (may be non-UTF-8)
	Detail string // human-readable detail; not part of the contract
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("invalid path %q: %s", e.Input, e.Kind)
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// Is makes errors.Is(err, ErrInvalidPath) true for every *Error.
func (e *Error) Is(target error) bool { return target == ErrInvalidPath }

// KindOf returns the Kind of err if it is (or wraps) an *Error, else "".
func KindOf(err error) Kind {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Kind
	}
	return ""
}

func fail(k Kind, input, detail string) error {
	return &Error{Kind: k, Input: input, Detail: detail}
}

// UnicodeVersion is V, the Unicode version whose tables decide at write time
// whether a code point is assigned. V is the older of the standard library's
// unicode.Version and the version of golang.org/x/text (NFC, case folding);
// the tests fail if the standard library ever becomes the newer one, because
// the assignment check uses the standard library's tables. Raising V only
// allows more characters; read-time checks never depend on it.
var UnicodeVersion = unicode.Version

// isControl reports C0 controls, DEL and C1 controls (general category Cc).
func isControl(r rune) bool { return r < 0x20 || 0x7f <= r && r <= 0x9f }

// isDisallowed reports the fixed set of bidirectional formatting characters
// (spoofing, "Trojan Source") and invisible zero-width characters rejected in
// names. U+200C (ZWNJ) and U+200D (ZWJ) are deliberately allowed: emoji
// sequences and several scripts need them.
func isDisallowed(r rune) bool {
	switch r {
	case 0x061c, // ARABIC LETTER MARK
		0x200e, 0x200f, // LEFT-TO-RIGHT / RIGHT-TO-LEFT MARK
		0x200b, // ZERO WIDTH SPACE
		0x2060, // WORD JOINER
		0xfeff: // ZERO WIDTH NO-BREAK SPACE (byte order mark)
		return true
	}
	return 0x202a <= r && r <= 0x202e || // embeddings and overrides LRE..RLO
		0x2066 <= r && r <= 0x2069 // isolates LRI..PDI
}

// isUnassigned reports code points of general category Cn in UnicodeVersion,
// including the noncharacters U+FDD0-U+FDEF and U+xxFFFE/U+xxFFFF.
func isUnassigned(r rune) bool { return unicode.Is(unicode.Cn, r) }

// Normalize validates a '/'-separated relative path and returns its canonical
// (NFC) form. These are the write-time rules, applied to every path that is
// uploaded or committed. The checks run in this order, and the first failure
// wins:
//
//  1. valid UTF-8 and non-empty;
//  2. no leading '/'; then the characters are scanned from left to right and
//     the first offending one decides: '\' (backslash), a control character
//     U+0000-U+001F or U+007F-U+009F (control_char), a bidi control or
//     zero-width character (disallowed_char), a code point unassigned in
//     UnicodeVersion (unassigned_char);
//  3. NFC normalization;
//  4. split on '/': no empty segments ("a//b", trailing '/'), no "." or "..";
//  5. every segment <= MaxSegmentBytes and the whole path <= MaxPathBytes,
//     counted in bytes of the NFC form.
//
// No other rules apply (in particular no platform-specific reserved names,
// and ZWNJ U+200C / ZWJ U+200D are allowed).
func Normalize(p string) (string, error) {
	if p == "" {
		return "", fail(KindEmpty, p, "")
	}
	if !utf8.ValidString(p) {
		return "", fail(KindInvalidUTF8, p, "")
	}
	if p[0] == '/' {
		return "", fail(KindAbsolute, p, "paths are relative to the workspace root")
	}
	if err := checkChars(p, p, true); err != nil {
		return "", err
	}
	n := norm.NFC.String(p)
	segs := strings.Split(n, "/")
	for _, s := range segs {
		switch s {
		case "":
			return "", fail(KindEmptySegment, p, "")
		case ".", "..":
			return "", fail(KindDotSegment, p, fmt.Sprintf("segment %q", s))
		}
	}
	for _, s := range segs {
		if len(s) > MaxSegmentBytes {
			return "", fail(KindSegmentTooLong, p, fmt.Sprintf("segment is %d bytes, limit %d", len(s), MaxSegmentBytes))
		}
	}
	if len(n) > MaxPathBytes {
		return "", fail(KindPathTooLong, p, fmt.Sprintf("path is %d bytes, limit %d", len(n), MaxPathBytes))
	}
	return n, nil
}

// checkChars scans the valid UTF-8 string s from left to right and rejects
// the first '\', control character or disallowed character; with
// assignedOnly it also rejects code points unassigned in UnicodeVersion (a
// write-time rule). input is reported as the offending value.
func checkChars(s, input string, assignedOnly bool) error {
	for _, r := range s {
		switch {
		case r == '\\':
			return fail(KindBackslash, input, "")
		case isControl(r):
			return fail(KindControlChar, input, fmt.Sprintf("%U", r))
		case isDisallowed(r):
			return fail(KindDisallowedChar, input, fmt.Sprintf("%U", r))
		case assignedOnly && isUnassigned(r):
			return fail(KindUnassignedChar, input, fmt.Sprintf("%U is not assigned in Unicode %s", r, UnicodeVersion))
		}
	}
	return nil
}

// NormalizeSegment validates a single path segment (a file or directory
// name) and returns its NFC form. It applies the same write-time rules as
// Normalize and additionally rejects '/' (checked before the character scan).
func NormalizeSegment(s string) (string, error) {
	if s == "" {
		return "", fail(KindEmpty, s, "")
	}
	if !utf8.ValidString(s) {
		return "", fail(KindInvalidUTF8, s, "")
	}
	if strings.IndexByte(s, '/') >= 0 {
		return "", fail(KindContainsSlash, s, "")
	}
	if err := checkChars(s, s, true); err != nil {
		return "", err
	}
	n := norm.NFC.String(s)
	if n == "." || n == ".." {
		return "", fail(KindDotSegment, s, "")
	}
	if len(n) > MaxSegmentBytes {
		return "", fail(KindSegmentTooLong, s, fmt.Sprintf("segment is %d bytes, limit %d", len(n), MaxSegmentBytes))
	}
	return n, nil
}

// IsNormalizedSegment reports whether s is a valid segment already in
// canonical form (NormalizeSegment(s) == s).
func IsNormalizedSegment(s string) bool {
	n, err := NormalizeSegment(s)
	return err == nil && n == s
}

// NormalizeBranchName validates a branch name with the single-segment rules
// (so it can never contain '/') and returns its NFC form, which is the form
// to store.
func NormalizeBranchName(s string) (string, error) { return NormalizeSegment(s) }

// ValidateBranchName reports whether s is acceptable as a branch name. Store
// NormalizeBranchName(s), not s.
func ValidateBranchName(s string) error {
	_, err := NormalizeSegment(s)
	return err
}

// folder implements Unicode full case folding. cases.Fold is documented to be
// stateless and safe for concurrent use.
var folder = cases.Fold()

// FoldKey returns the case-insensitive comparison key of a name: NFC, then
// Unicode full case folding. Two siblings with the same FoldKey conflict.
func FoldKey(name string) string {
	return folder.String(norm.NFC.String(name))
}

// Sibling is one name in a directory, used by CheckSiblings.
type Sibling struct {
	Name string
	Dir  bool
}

// CheckSiblings rejects a set of names that cannot coexist in one directory:
// the same name used as both a file and a directory (KindFileDirConflict),
// the same name twice (KindDuplicate), or distinct names with the same
// FoldKey (KindCaseConflict). Names are expected to be normalized. The
// reported conflict is deterministic: the byte-wise smallest offending name
// wins.
func CheckSiblings(entries []Sibling) error {
	s := make([]Sibling, len(entries))
	copy(s, entries)
	sort.SliceStable(s, func(i, j int) bool { return s[i].Name < s[j].Name })
	for i := 1; i < len(s); i++ {
		if s[i].Name != s[i-1].Name {
			continue
		}
		if s[i].Dir != s[i-1].Dir {
			return fail(KindFileDirConflict, s[i].Name, "used as both a file and a directory")
		}
		return fail(KindDuplicate, s[i].Name, "")
	}
	seen := make(map[string]string, len(s))
	for _, e := range s {
		k := FoldKey(e.Name)
		if prev, ok := seen[k]; ok {
			return fail(KindCaseConflict, e.Name, fmt.Sprintf("conflicts with %q", prev))
		}
		seen[k] = e.Name
	}
	return nil
}
