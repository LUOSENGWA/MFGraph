package extref_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/actorref"
	"github.com/LUOSENGWA/MFGraph/internal/extref"
)

func TestAcceptedAndRejected(t *testing.T) {
	long := "wf-" + strings.Repeat("x", extref.MaxBytes-3) // 255 bytes, not a ULID
	for _, s := range []string{
		long, "workflow/run:2026-11-14#0001", "01JD3X5Q8Z9W2V4T6R8P0N2M4K", "a", "a ", "\u5de5\u4f5c\u6d41-7",
	} {
		if err := extref.Validate(s); err != nil || !extref.Valid(s) || extref.Problem(s) != "" {
			t.Errorf("Validate(%q) = %v", s, err)
		}
	}
	for name, s := range map[string]string{
		"empty": "", "256 bytes": long + "x", "newline": "wf\n1", "del": "wf\x7f", "c1": "wf\u0085",
		"invalid utf-8": "wf\xff", "truncated": "wf\xc3",
	} {
		err := extref.Validate(s)
		if err == nil || extref.Valid(s) || !errors.Is(err, extref.ErrInvalid) {
			t.Errorf("%s: Validate(%q) = %v", name, s, err)
		}
		if s != "" && strings.Contains(err.Error(), s) {
			t.Errorf("%s: the error repeats the value", name)
		}
	}
}

// TestActorReferencesAreExternalReferences: both packages apply one rule.
func TestActorReferencesAreExternalReferences(t *testing.T) {
	for _, s := range []string{"", "manager", "a\nb", "a\xffb", strings.Repeat("x", 255), strings.Repeat("x", 256), "@alice:example.com"} {
		if extref.Valid(s) != actorref.Valid(s) {
			t.Errorf("%q: extref %v, actorref %v", s, extref.Valid(s), actorref.Valid(s))
		}
	}
	if extref.MaxBytes != actorref.MaxBytes {
		t.Errorf("MaxBytes differ")
	}
}
