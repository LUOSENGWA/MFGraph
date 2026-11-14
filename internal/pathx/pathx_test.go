package pathx_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/LUOSENGWA/MFGraph/internal/pathx"
)

type genSpec struct {
	Segments []struct {
		Unit   string `json:"unit"`
		Repeat int    `json:"repeat"`
		Times  int    `json:"times"`
	} `json:"segments"`
}

func (g *genSpec) build() string {
	var segs []string
	for _, s := range g.Segments {
		times := s.Times
		if times == 0 {
			times = 1
		}
		for i := 0; i < times; i++ {
			segs = append(segs, strings.Repeat(s.Unit, s.Repeat))
		}
	}
	return strings.Join(segs, "/")
}

type ioCase struct {
	Name      string   `json:"name"`
	Input     *string  `json:"input"`
	InputHex  *string  `json:"input_hex"`
	InputGen  *genSpec `json:"input_gen"`
	Output    *string  `json:"output"`
	OutputGen *genSpec `json:"output_gen"`
	Error     string   `json:"error"`
	// UCDVersion marks a case whose result depends on the Unicode version
	// (assigned or not in V); it must equal pathx.UnicodeVersion.
	UCDVersion string `json:"ucd_version"`
}

func (c ioCase) input(t *testing.T) string {
	t.Helper()
	switch {
	case c.Input != nil:
		return *c.Input
	case c.InputHex != nil:
		b, err := hex.DecodeString(*c.InputHex)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	case c.InputGen != nil:
		return c.InputGen.build()
	}
	t.Fatalf("case %q has no input", c.Name)
	return ""
}

func (c ioCase) output() string {
	if c.OutputGen != nil {
		return c.OutputGen.build()
	}
	if c.Output != nil {
		return *c.Output
	}
	return ""
}

type vectorFile struct {
	Schema       string   `json:"schema"`
	ReviewStatus string   `json:"review_status"`
	Description  string   `json:"description"`
	Normalize    []ioCase `json:"normalize"`
	Fold         []struct {
		Input string `json:"input"`
		Fold  string `json:"fold"`
	} `json:"fold"`
	Siblings []struct {
		Name    string          `json:"name"`
		Entries []pathx.Sibling `json:"entries"`
		Error   string          `json:"error"`
	} `json:"siblings"`
	BranchNames []ioCase `json:"branch_names"`
	StoredNames []ioCase `json:"stored_names"`
}

func loadVectors(t *testing.T) vectorFile {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "pathx.json"))
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range b {
		if c >= 0x80 || c == 0x7f {
			t.Fatalf("pathx.json: raw non-ASCII byte at offset %d: vector files are ASCII, characters are \\u escapes (run: python3 scripts/vectors.py format)", i)
		}
	}
	var v vectorFile
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode vectors: %v", err)
	}
	return v
}

func checkResult(t *testing.T, label string, c ioCase, got string, err error) {
	t.Helper()
	if c.Error != "" {
		if err == nil {
			t.Errorf("%s: got %q, want error %s", label, got, c.Error)
			return
		}
		if k := pathx.KindOf(err); string(k) != c.Error {
			t.Errorf("%s: error kind %q (%v), want %s", label, k, err, c.Error)
		}
		if !errors.Is(err, pathx.ErrInvalidPath) {
			t.Errorf("%s: error does not match ErrInvalidPath", label)
		}
		return
	}
	if err != nil {
		t.Errorf("%s: unexpected error %v", label, err)
		return
	}
	if want := c.output(); got != want {
		t.Errorf("%s: got %q, want %q", label, got, want)
	}
}

// checkUCD fails for a case tagged with a Unicode version other than V, and
// counts the tagged cases.
func checkUCD(t *testing.T, cases []ioCase) int {
	t.Helper()
	n := 0
	for _, c := range cases {
		if c.UCDVersion == "" {
			continue
		}
		if c.UCDVersion != pathx.UnicodeVersion {
			t.Fatalf("case %q needs Unicode %s, V is %s", c.Name, c.UCDVersion, pathx.UnicodeVersion)
		}
		n++
	}
	return n
}

func TestNormalizeVectors(t *testing.T) {
	v := loadVectors(t)
	if len(v.Normalize) == 0 {
		t.Fatal("no normalize vectors")
	}
	// The cases tagged ucd_version pin V: U+1F6DC is assigned in 15.0, while
	// U+2EBF0, U+1C89 and U+1F6D8 are assigned only later.
	if n := checkUCD(t, v.Normalize) + checkUCD(t, v.BranchNames); n < 5 {
		t.Fatalf("only %d cases pin V", n)
	}
	for _, c := range v.Normalize {
		got, err := pathx.Normalize(c.input(t))
		checkResult(t, "Normalize "+c.Name, c, got, err)
		if err == nil {
			// Normalization is idempotent.
			again, err2 := pathx.Normalize(got)
			if err2 != nil || again != got {
				t.Errorf("Normalize %s: not idempotent: %q %v", c.Name, again, err2)
			}
		}
	}
}

func TestFoldVectors(t *testing.T) {
	v := loadVectors(t)
	for _, c := range v.Fold {
		if got := pathx.FoldKey(c.Input); got != c.Fold {
			t.Errorf("FoldKey(%+q) = %+q, want %+q", c.Input, got, c.Fold)
		}
	}
}

func TestSiblingVectors(t *testing.T) {
	v := loadVectors(t)
	for _, c := range v.Siblings {
		err := pathx.CheckSiblings(c.Entries)
		switch {
		case c.Error == "" && err != nil:
			t.Errorf("%s: unexpected error %v", c.Name, err)
		case c.Error != "" && string(pathx.KindOf(err)) != c.Error:
			t.Errorf("%s: got %v, want %s", c.Name, err, c.Error)
		}
		// The verdict must not depend on input order.
		rev := make([]pathx.Sibling, len(c.Entries))
		for i, e := range c.Entries {
			rev[len(rev)-1-i] = e
		}
		if pathx.KindOf(pathx.CheckSiblings(rev)) != pathx.KindOf(err) {
			t.Errorf("%s: verdict depends on order", c.Name)
		}
	}
}

func TestBranchNameVectors(t *testing.T) {
	v := loadVectors(t)
	for _, c := range v.BranchNames {
		in := c.input(t)
		got, err := pathx.NormalizeBranchName(in)
		checkResult(t, "branch "+strings.ToValidUTF8(in, "?"), c, got, err)
		if (pathx.ValidateBranchName(in) == nil) != (err == nil) {
			t.Errorf("ValidateBranchName(%q) disagrees with NormalizeBranchName", in)
		}
	}
}

func TestNormalizeSegment(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want pathx.Kind
	}{
		{"a", ""}, {"a/b", pathx.KindContainsSlash}, {"..", pathx.KindDotSegment},
		{"", pathx.KindEmpty}, {"\x00", pathx.KindControlChar}, {"\\", pathx.KindBackslash},
	} {
		_, err := pathx.NormalizeSegment(tc.in)
		if pathx.KindOf(err) != tc.want {
			t.Errorf("NormalizeSegment(%q) = %v, want %q", tc.in, err, tc.want)
		}
	}
	if !pathx.IsNormalizedSegment("caf\u00e9") || pathx.IsNormalizedSegment("cafe\u0301") {
		t.Error("IsNormalizedSegment must accept NFC and reject NFD")
	}
}

func TestErrorType(t *testing.T) {
	_, err := pathx.Normalize("../x")
	var pe *pathx.Error
	if !errors.As(err, &pe) || pe.Kind != pathx.KindDotSegment || pe.Input != "../x" {
		t.Fatalf("err = %#v", err)
	}
	if !strings.Contains(err.Error(), "dot_segment") {
		t.Fatalf("message %q lacks kind", err.Error())
	}
	if pathx.KindOf(errors.New("other")) != "" || pathx.KindOf(nil) != "" {
		t.Fatal("KindOf of foreign error must be empty")
	}
}

// TestUnicodeVersionV guards V. The write-time rules use the tables of the
// standard library (general categories) and of golang.org/x/text (NFC, case
// folding); all of them must be exactly V, and V must be the number written
// in the specification (both languages). A Go or x/text upgrade that changes
// the tables makes this test fail: raise V only by updating the
// specification and the vectors tagged with ucd_version.
func TestUnicodeVersionV(t *testing.T) {
	for name, v := range map[string]string{
		"standard library unicode": unicode.Version,
		"x/text unicode/norm":      norm.Version,
		"x/text cases":             cases.UnicodeVersion,
	} {
		if v != pathx.UnicodeVersion {
			t.Errorf("%s has Unicode %s, but V = %s (docs/spec/content-format.md, section 2.4)", name, v, pathx.UnicodeVersion)
		}
	}
	re := regexp.MustCompile(`\*\*V = ([0-9]+\.[0-9]+\.[0-9]+)\*\*`)
	for _, spec := range []string{"content-format.md", filepath.Join("zh-CN", "content-format.md")} {
		b, err := os.ReadFile(filepath.Join("..", "..", "docs", "spec", spec))
		if err != nil {
			t.Fatal(err)
		}
		m := re.FindAllSubmatch(b, -1)
		if len(m) == 0 {
			t.Errorf("%s does not state **V = x.y.z**", spec)
		}
		for _, x := range m {
			if string(x[1]) != pathx.UnicodeVersion {
				t.Errorf("%s states V = %s, code has %s", spec, x[1], pathx.UnicodeVersion)
			}
		}
	}
}

func TestAllowedAndRejectedCharacters(t *testing.T) {
	allowed := []rune{0x200c, 0x200d, 0xff0f, 0xe000, 0x00a0, 0xfe00, 0xfe0e, 0xfe0f, 0xe0020, 0xe0067, 0xe007f, 0x034f}
	for _, r := range allowed {
		if _, err := pathx.NormalizeSegment("a" + string(r) + "b"); err != nil {
			t.Errorf("%U must be allowed: %v", r, err)
		}
	}
	rejected := map[rune]pathx.Kind{
		0x0085: pathx.KindControlChar, 0x0080: pathx.KindControlChar, 0x009f: pathx.KindControlChar,
		0x061c: pathx.KindDisallowedChar, 0x200e: pathx.KindDisallowedChar, 0x200f: pathx.KindDisallowedChar,
		0x202a: pathx.KindDisallowedChar, 0x202e: pathx.KindDisallowedChar, 0x2066: pathx.KindDisallowedChar,
		0x2069: pathx.KindDisallowedChar, 0x200b: pathx.KindDisallowedChar, 0x2060: pathx.KindDisallowedChar,
		0xfeff: pathx.KindDisallowedChar, 0x00ad: pathx.KindDisallowedChar, 0x180e: pathx.KindDisallowedChar,
		0x2028: pathx.KindDisallowedChar, 0x2029: pathx.KindDisallowedChar, 0x2061: pathx.KindDisallowedChar,
		0x2062: pathx.KindDisallowedChar, 0x2063: pathx.KindDisallowedChar, 0x2064: pathx.KindDisallowedChar,
		0xfff9: pathx.KindDisallowedChar, 0xfffa: pathx.KindDisallowedChar, 0xfffb: pathx.KindDisallowedChar,
		0x0378: pathx.KindUnassignedChar, 0x1fffd: pathx.KindUnassignedChar,
		0xfffe: pathx.KindNoncharacter, 0xfdd0: pathx.KindNoncharacter, 0x10ffff: pathx.KindNoncharacter,
	}
	for r, want := range rejected {
		if _, err := pathx.NormalizeSegment("a" + string(r)); pathx.KindOf(err) != want {
			t.Errorf("%U: got %v, want %s", r, err, want)
		}
		// Read time keeps only the structural rules: control characters and
		// noncharacters; the disallowed list and unassigned code points are
		// readable.
		readWant := want
		if want == pathx.KindDisallowedChar || want == pathx.KindUnassignedChar {
			readWant = ""
		}
		if err := pathx.ValidateStoredName("a" + string(r)); pathx.KindOf(err) != readWant {
			t.Errorf("%U at read time: got %v, want %q", r, err, readWant)
		}
	}
	for _, r := range allowed {
		if err := pathx.ValidateStoredName("a" + string(r) + "b"); err != nil {
			t.Errorf("%U must be readable: %v", r, err)
		}
	}
}

// TestNoncharacters: the 66 noncharacters are rejected with their own kind at
// write time and at read time; their neighbours are not noncharacters.
func TestNoncharacters(t *testing.T) {
	n := 0
	for r := rune(0); r <= unicode.MaxRune; r++ {
		nonchar := 0xfdd0 <= r && r <= 0xfdef || r&0xfffe == 0xfffe
		if !nonchar {
			continue
		}
		n++
		name := "x" + string(r)
		if _, err := pathx.NormalizeSegment(name); pathx.KindOf(err) != pathx.KindNoncharacter {
			t.Errorf("NormalizeSegment %U: %v", r, err)
		}
		if _, err := pathx.Normalize("d/" + name); pathx.KindOf(err) != pathx.KindNoncharacter {
			t.Errorf("Normalize %U: %v", r, err)
		}
		if err := pathx.ValidateStoredName(name); pathx.KindOf(err) != pathx.KindNoncharacter {
			t.Errorf("ValidateStoredName %U: %v", r, err)
		}
		if !unicode.Is(unicode.Cn, r) {
			t.Errorf("%U: noncharacters are unassigned (Cn)", r)
		}
	}
	if n != 66 {
		t.Fatalf("%d noncharacters, want 66", n)
	}
	for _, r := range []rune{0xfdcf, 0xfdf0, 0xfffd, 0x1fffd, 0x10fffd} {
		if err := pathx.ValidateStoredName("x" + string(r)); err != nil {
			t.Errorf("%U is not a noncharacter: %v", r, err)
		}
	}
}

// TestFoldKeyExhaustive checks FoldKey against the standard library's Unicode
// tables (version V, see TestUnicodeVersionV) for every assigned code point c:
//
//  1. FoldKey is idempotent: FoldKey(FoldKey(c)) == FoldKey(c);
//  2. every character of the unicode.SimpleFold orbit of c has the same
//     FoldKey as c.
//
// A case folding that disagrees with CaseFolding.txt breaks one of the two,
// for example the Cherokee folding of golang.org/x/text (which swaps the two
// cases and fails both checks for 172 code points without the correction in
// FoldKey).
func TestFoldKeyExhaustive(t *testing.T) {
	idem, orbit := 0, 0
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if 0xd800 <= r && r <= 0xdfff || unicode.Is(unicode.Cn, r) {
			continue // surrogates are not valid UTF-8; unassigned code points are rejected at write time
		}
		k := pathx.FoldKey(string(r))
		if again := pathx.FoldKey(k); again != k {
			if idem++; idem <= 10 {
				t.Errorf("FoldKey not idempotent for %U: %+q, then %+q", r, k, again)
			}
		}
		for o := unicode.SimpleFold(r); o != r; o = unicode.SimpleFold(o) {
			if ko := pathx.FoldKey(string(o)); ko != k {
				if orbit++; orbit <= 10 {
					t.Errorf("SimpleFold orbit of %U: FoldKey(%U) = %+q, FoldKey(%U) = %+q", r, r, k, o, ko)
				}
			}
		}
	}
	if idem+orbit > 0 {
		t.Errorf("%d idempotence and %d orbit failures", idem, orbit)
	}
}

// TestFoldKeyCherokeeAndCanonicalEquivalence pins the cases that the former
// key (NFC, then golang.org/x/text folding) got wrong.
func TestFoldKeyCherokeeAndCanonicalEquivalence(t *testing.T) {
	for _, pair := range [][2]string{
		{"\u13a0", "\uab70"},        // CHEROKEE LETTER A / SMALL LETTER A
		{"\u13f0", "\u13f8"},        // CHEROKEE LETTER YE / SMALL LETTER YE
		{"\u13ef", "\uabbf"},        // last pair of the U+AB70 block
		{"\u1e9e\u0301", "s\u015b"}, // capital sharp s + acute / s + s with acute
		{"\ufb01\u0301", "f\u00ed"}, // fi ligature + acute / f + i with acute
		{"\u1c90", "\u10d0"},        // Georgian Mtavruli / Mkhedruli
	} {
		if a, b := pathx.FoldKey(pair[0]), pathx.FoldKey(pair[1]); a != b {
			t.Errorf("FoldKey(%+q) = %+q, FoldKey(%+q) = %+q: want equal", pair[0], a, pair[1], b)
		}
	}
	if got := pathx.FoldKey("\uab70"); got != "\u13a0" {
		t.Errorf("Cherokee folds to the uppercase letter (CaseFolding.txt): got %+q", got)
	}
	if pathx.FoldKey("\u0131") == pathx.FoldKey("I") {
		t.Error("dotless i U+0131 and I must not fold together (no Turkic mappings)")
	}
}

// TestStoredNameVectors checks the read-time rules for names in stored nodes.
func TestStoredNameVectors(t *testing.T) {
	v := loadVectors(t)
	if len(v.StoredNames) == 0 {
		t.Fatal("no stored_names vectors")
	}
	for _, c := range v.StoredNames {
		err := pathx.ValidateStoredName(c.input(t))
		if got := string(pathx.KindOf(err)); got != c.Error || (c.Error == "" && err != nil) {
			t.Errorf("stored name %s: got %v, want %q", c.Name, err, c.Error)
		}
	}
}

// TestReadTimeIsSubsetOfWriteTime: every name the write path produces is
// readable.
func TestReadTimeIsSubsetOfWriteTime(t *testing.T) {
	v := loadVectors(t)
	for _, c := range append(v.Normalize, v.BranchNames...) {
		if c.Error != "" {
			continue
		}
		for _, seg := range strings.Split(c.output(), "/") {
			if err := pathx.ValidateStoredName(seg); err != nil {
				t.Errorf("%s: normalized segment %q fails the read-time rules: %v", c.Name, seg, err)
			}
		}
	}
}
