package pathx_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
}

func loadVectors(t *testing.T) vectorFile {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "pathx.json"))
	if err != nil {
		t.Fatal(err)
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

func TestNormalizeVectors(t *testing.T) {
	v := loadVectors(t)
	if len(v.Normalize) == 0 {
		t.Fatal("no normalize vectors")
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
			t.Errorf("FoldKey(%q) = %q, want %q", c.Input, got, c.Fold)
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
	if !pathx.IsNormalizedSegment("café") || pathx.IsNormalizedSegment("café") {
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
