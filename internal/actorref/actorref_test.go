package actorref_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/actorref"
)

// A worker name of 253 characters (DNS-label style), an ID of the form
// @user:server with exactly 255 bytes, and a ULID.
var (
	worker253 = "worker-" + strings.Repeat("a", 243) + "-01"
	id255     = "@" + strings.Repeat("u", 255-len("@:example.com")) + ":example.com"
	ulid      = "01JD3X5Q8Z9W2V4T6R8P0N2M4K"
)

func TestAccepted(t *testing.T) {
	if len(worker253) != 253 || len(id255) != 255 {
		t.Fatalf("test values have %d and %d bytes", len(worker253), len(id255))
	}
	for _, ref := range []string{
		"manager",
		worker253,
		"@alice:example.com",
		id255,
		ulid,
		"a",
		"a ",                          // trailing space: a different actor than "a"
		" ",                           // only the listed rules apply
		"\u5f20\u4e09",                // non-ASCII
		strings.Repeat("\u4e2d", 85),  // 255 bytes of CJK
		"cafe\u0301",                  // not normalized, kept as is
		"svc:build/agent=1,team@corp", // punctuation is fine
		"\U0001f916 bot",              // emoji
		strings.Repeat("x", actorref.MaxBytes),
	} {
		if err := actorref.Validate(ref); err != nil || !actorref.Valid(ref) {
			t.Errorf("Validate(%q) = %v, want nil", ref, err)
		}
	}
}

func TestRejected(t *testing.T) {
	for name, ref := range map[string]string{
		"empty":          "",
		"256 bytes":      strings.Repeat("x", actorref.MaxBytes+1),
		"256 bytes id":   id255 + "m",
		"258 bytes CJK":  strings.Repeat("\u4e2d", 86),
		"newline":        "alice\n",
		"tab":            "a\tb",
		"nul":            "a\x00",
		"del":            "a\x7f",
		"c1 control":     "a\u0085b",
		"invalid utf-8":  "a\xffb",
		"truncated":      "a\xc3",
		"encoded surrog": "a\xed\xa0\x80",
	} {
		err := actorref.Validate(ref)
		if err == nil || actorref.Valid(ref) {
			t.Errorf("%s: Validate(%q) accepted", name, ref)
			continue
		}
		if !errors.Is(err, actorref.ErrInvalid) {
			t.Errorf("%s: error %v does not match ErrInvalid", name, err)
		}
		if ref != "" && strings.Contains(err.Error(), ref) {
			t.Errorf("%s: error repeats the value: %v", name, err)
		}
	}
}

// TestByteExact: references are compared as bytes; nothing is normalized.
func TestByteExact(t *testing.T) {
	pairs := [][2]string{{"a", "a "}, {"Alice", "alice"}, {"caf\u00e9", "cafe\u0301"}}
	for _, p := range pairs {
		if !actorref.Valid(p[0]) || !actorref.Valid(p[1]) || p[0] == p[1] {
			t.Errorf("%q and %q must both be valid and different", p[0], p[1])
		}
	}
}

// TestPseudonym: the logged pseudonym is the first 16 hex digits of the
// SHA-256 of the reference, stable for one actor and different for others.
func TestPseudonym(t *testing.T) {
	sum := sha256.Sum256([]byte("manager"))
	if got := actorref.Pseudonym("manager"); got != hex.EncodeToString(sum[:])[:16] || len(got) != 16 {
		t.Fatalf("Pseudonym(manager) = %q", got)
	}
	if actorref.Pseudonym("a") == actorref.Pseudonym("a ") || actorref.Pseudonym("x") != actorref.Pseudonym("x") {
		t.Fatal("pseudonyms must be stable and byte exact")
	}
	if strings.Contains(actorref.Pseudonym("alice"), "alice") {
		t.Fatal("the pseudonym shows the reference")
	}
}
