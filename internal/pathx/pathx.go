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
	// its NFC UTF-8 form (at read time: as stored). It is the file name limit
	// of Linux ext4 and leaves room for 85 CJK characters. It is a read-time
	// rule too, so format 1 can never raise it.
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
	KindNoncharacter    Kind = "noncharacter"      // contains a noncharacter: U+FDD0-U+FDEF, U+xxFFFE, U+xxFFFF
	KindDisallowedChar  Kind = "disallowed_char"   // contains a character of the fixed disallowed list (bidi controls, invisible characters; write time only)
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

// UnicodeVersion is V, the Unicode version of the write-time rules: whether a
// code point is assigned, NFC and case folding. Format 1 fixes V at 15.0.0
// (docs/spec/content-format.md, section 2.4). The standard library
// (general categories, unicode.SimpleFold) and golang.org/x/text (NFC, case
// folding) must both provide exactly these tables; TestUnicodeVersionV fails
// otherwise, for example after a Go upgrade. Raising V means recording the
// new version in the specification; it only allows more characters, and the
// read-time rules never depend on it.
const UnicodeVersion = "15.0.0"

// isControl reports C0 controls, DEL and C1 controls (general category Cc).
func isControl(r rune) bool { return r < 0x20 || 0x7f <= r && r <= 0x9f }

// isNoncharacter reports the 66 noncharacters: U+FDD0-U+FDEF and the last
// two code points of every plane (U+xxFFFE, U+xxFFFF). They are rejected at
// write time and at read time: I-JSON (RFC 7493), on which RFC 8785 relies,
// forbids them, and they are fixed code points, so the check needs no Unicode
// tables.
func isNoncharacter(r rune) bool { return 0xfdd0 <= r && r <= 0xfdef || r&0xfffe == 0xfffe }

// isDisallowed reports the fixed list of characters rejected in names at
// write time (they are readable: the read-time rules do not check them):
//
//   - bidirectional formatting characters (spoofing, "Trojan Source"):
//     U+061C, U+200E, U+200F, U+202A-U+202E, U+2066-U+2069;
//   - invisible or layout-breaking characters: U+00AD SOFT HYPHEN, U+180E
//     MONGOLIAN VOWEL SEPARATOR, U+200B ZERO WIDTH SPACE, U+2028 LINE
//     SEPARATOR, U+2029 PARAGRAPH SEPARATOR, U+2060 WORD JOINER,
//     U+2061-U+2064 invisible mathematical operators, U+FEFF ZERO WIDTH
//     NO-BREAK SPACE, U+FFF9-U+FFFB interlinear annotation controls.
//
// It is a fixed list, not the whole format category Cf. Deliberately allowed:
// U+200C ZWNJ and U+200D ZWJ (emoji sequences, several scripts), the
// variation selectors U+FE00-U+FE0F and the tag characters U+E0020-U+E007F
// (emoji presentation and subdivision flags), and U+034F COMBINING GRAPHEME
// JOINER.
func isDisallowed(r rune) bool {
	switch r {
	case 0x00ad, // SOFT HYPHEN
		0x061c,         // ARABIC LETTER MARK
		0x180e,         // MONGOLIAN VOWEL SEPARATOR
		0x200b,         // ZERO WIDTH SPACE
		0x200e, 0x200f, // LEFT-TO-RIGHT / RIGHT-TO-LEFT MARK
		0x2028, 0x2029, // LINE / PARAGRAPH SEPARATOR
		0x2060, // WORD JOINER
		0xfeff: // ZERO WIDTH NO-BREAK SPACE (byte order mark)
		return true
	}
	return 0x202a <= r && r <= 0x202e || // embeddings and overrides LRE..RLO
		0x2061 <= r && r <= 0x2064 || // invisible mathematical operators
		0x2066 <= r && r <= 0x2069 || // isolates LRI..PDI
		0xfff9 <= r && r <= 0xfffb // interlinear annotation controls
}

// isUnassigned reports code points of general category Cn in UnicodeVersion.
// The noncharacters are Cn too, but checkChars reports them as
// KindNoncharacter first.
func isUnassigned(r rune) bool { return unicode.Is(unicode.Cn, r) }

// Normalize validates a '/'-separated relative path and returns its canonical
// (NFC) form. These are the write-time rules, applied to every path that is
// uploaded or committed. The checks run in this order, and the first failure
// wins:
//
//  1. valid UTF-8 and non-empty;
//  2. no leading '/'; then the characters are scanned from left to right and
//     the first offending one decides: '\' (backslash), a control character
//     U+0000-U+001F or U+007F-U+009F (control_char), a noncharacter
//     (noncharacter), a character of the fixed disallowed list: bidi
//     controls and invisible characters (disallowed_char), a code point
//     unassigned in UnicodeVersion (unassigned_char);
//  3. NFC normalization;
//  4. split on '/': no empty segments ("a//b", trailing '/'), no "." or "..";
//  5. every segment <= MaxSegmentBytes and the whole path <= MaxPathBytes,
//     counted in bytes of the NFC form.
//
// No other rules apply (in particular no platform-specific reserved names;
// ZWNJ, ZWJ, variation selectors, tag characters and U+034F are allowed).
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
// the first '\', control character or noncharacter (the read-time character
// rules); with writeTime it also rejects the fixed disallowed list and code
// points unassigned in UnicodeVersion. input is reported as the offending
// value.
func checkChars(s, input string, writeTime bool) error {
	for _, r := range s {
		switch {
		case r == '\\':
			return fail(KindBackslash, input, "")
		case isControl(r):
			return fail(KindControlChar, input, fmt.Sprintf("%U", r))
		case isNoncharacter(r):
			return fail(KindNoncharacter, input, fmt.Sprintf("%U", r))
		case writeTime && isDisallowed(r):
			return fail(KindDisallowedChar, input, fmt.Sprintf("%U", r))
		case writeTime && isUnassigned(r):
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

// ValidateStoredName applies the read-time rules to a name found in a stored
// tree node (an entry name or the first name of an index reference). They are
// structural and never depend on Unicode tables:
//
//  1. valid UTF-8 (invalid_utf8) and non-empty (empty);
//  2. no '/' (contains_slash);
//  3. scanning from left to right, no '\' (backslash), no control character
//     U+0000-U+001F or U+007F-U+009F (control_char), no noncharacter
//     (noncharacter);
//  4. not "." or ".." (dot_segment);
//  5. at most MaxSegmentBytes bytes as stored (segment_too_long).
//
// It deliberately does not check NFC, case-fold conflicts, whether code
// points are assigned, or the fixed disallowed list: those are write-time
// rules, which may be adjusted later, and a stored tree must stay readable
// by every reader of format 1.
func ValidateStoredName(name string) error {
	if name == "" {
		return fail(KindEmpty, name, "")
	}
	if !utf8.ValidString(name) {
		return fail(KindInvalidUTF8, name, "")
	}
	if strings.IndexByte(name, '/') >= 0 {
		return fail(KindContainsSlash, name, "")
	}
	if err := checkChars(name, name, false); err != nil {
		return err
	}
	if name == "." || name == ".." {
		return fail(KindDotSegment, name, "")
	}
	if len(name) > MaxSegmentBytes {
		return fail(KindSegmentTooLong, name, fmt.Sprintf("name is %d bytes, limit %d", len(name), MaxSegmentBytes))
	}
	return nil
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

// folder implements Unicode full case folding (CaseFolding.txt statuses C
// and F, without the Turkic mappings). cases.Fold is documented to be
// stateless and safe for concurrent use.
var folder = cases.Fold()

// FoldKey returns the case-insensitive comparison key of a name: the
// canonical caseless match of the Unicode Standard (section 3.13, D145),
//
//	NFD(casefold(NFD(name)))
//
// where casefold is the full case folding of CaseFolding.txt (statuses C and
// F). Two siblings with the same FoldKey conflict. The key is only compared,
// never stored, so its NFD form does not matter. TestFoldKeyExhaustive checks
// it against the Unicode tables of version V for every assigned code point.
func FoldKey(name string) string {
	return norm.NFD.String(foldCherokee(folder.String(norm.NFD.String(name))))
}

// foldCherokee corrects the case folding of golang.org/x/text for Cherokee,
// which swaps the two cases instead of folding both to the uppercase letters
// as CaseFolding.txt does. Mapping the 86 lowercase letters U+AB70-U+ABBF and
// U+13F8-U+13FD to U+13A0-U+13EF and U+13F0-U+13F5 after x/text's folding
// gives the CaseFolding.txt result for either input case (and is a no-op if a
// later x/text version folds Cherokee correctly).
func foldCherokee(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case 0xab70 <= r && r <= 0xabbf:
			return r - 0xab70 + 0x13a0
		case 0x13f8 <= r && r <= 0x13fd:
			return r - 0x13f8 + 0x13f0
		}
		return r
	}, s)
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
