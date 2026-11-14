package storagekey_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/ids"
	"github.com/LUOSENGWA/MFGraph/internal/storagekey"
)

func TestKeys(t *testing.T) {
	h := contenthash.MustParse("sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824")
	if got := storagekey.ObjectKey(h); got != "objects/2c/2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Errorf("ObjectKey = %s", got)
	}
	if got := storagekey.TreeKey(h); got != "tree/2c/2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824.json.zst" {
		t.Errorf("TreeKey = %s", got)
	}
	defer func() {
		if recover() == nil {
			t.Error("zero hash must panic")
		}
	}()
	storagekey.ObjectKey(contenthash.Hash{})
}

// TestMaxPrefixBytes: the longest valid prefix plus either relative key fits
// the 1024-byte key limit of S3, and one byte more is rejected.
func TestMaxPrefixBytes(t *testing.T) {
	if storagekey.MaxPrefixBytes != 942 || storagekey.MaxKeyBytes != 1024 {
		t.Fatalf("MaxPrefixBytes = %d, MaxKeyBytes = %d", storagekey.MaxPrefixBytes, storagekey.MaxKeyBytes)
	}
	h := contenthash.SumBytes([]byte("x"))
	// 942 bytes: 3 segments of 255 bytes and one of 174, joined by '/'.
	longest := strings.Join([]string{strings.Repeat("a", 255), strings.Repeat("b", 255), strings.Repeat("c", 255), strings.Repeat("d", 174)}, "/")
	if len(longest) != storagekey.MaxPrefixBytes {
		t.Fatalf("test prefix has %d bytes", len(longest))
	}
	if err := storagekey.ValidatePrefix(longest); err != nil {
		t.Fatalf("longest prefix rejected: %v", err)
	}
	for _, key := range []string{storagekey.TreeKey(h), storagekey.ObjectKey(h)} {
		if full := storagekey.Join(longest, key); len(full) > storagekey.MaxKeyBytes {
			t.Errorf("%s: full key has %d bytes", key, len(full))
		}
	}
	if full := storagekey.Join(longest, storagekey.TreeKey(h)); len(full) != storagekey.MaxKeyBytes {
		t.Errorf("the tree key of the longest prefix has %d bytes, want exactly %d", len(full), storagekey.MaxKeyBytes)
	}
	if err := storagekey.ValidatePrefix(longest + "d"); !errors.Is(err, storagekey.ErrInvalidPrefix) {
		t.Errorf("943-byte prefix: %v", err)
	}
}

func TestValidatePrefix(t *testing.T) {
	ok := []string{
		"projects/01JD00000000000000000000P0", "a", "tenants/t/projects/p/workspaces/w",
		"A-b_c.d/E9", "..a/b..", "-", "_", "...", "v1.2", "Tenants/A",
	}
	for _, p := range ok {
		if err := storagekey.ValidatePrefix(p); err != nil {
			t.Errorf("ValidatePrefix(%q) = %v", p, err)
		}
	}
	bad := []string{
		"", "/a", "a/", "/", "a//b", "a/../b", "a/./b", ".", "..", "a/..", "a\\b", "a/\x00", "a\x7f",
		"café", "文档/空间", "a b", "a#b", "a?b", "a%20b", "a+b", "a:b", "a~b", "a*b",
		"aKb", strings.Repeat("a", storagekey.MaxPrefixBytes+1),
	}
	for _, p := range bad {
		if err := storagekey.ValidatePrefix(p); !errors.Is(err, storagekey.ErrInvalidPrefix) {
			t.Errorf("ValidatePrefix(%q) = %v, want ErrInvalidPrefix", p, err)
		}
	}
}

func TestPrefixesConflict(t *testing.T) {
	for _, tc := range []struct {
		a, b     string
		conflict bool
	}{
		{"a", "a", true},
		{"tenants/A", "Tenants/a", true}, // ASCII case-insensitive
		{"a", "a/b", true},               // nested
		{"A", "a/b/c", true},
		{"ws/x/y", "WS/X", true},
		{"a", "ab", false}, // not a path ancestor
		{"a/b", "a/bc", false},
		{"a/b", "a/c", false},
		{"a-b", "a", false},
		{"a.b", "a", false},
		{"tenants/T/projects/P/workspaces/W1", "tenants/T/projects/P/workspaces/W2", false},
	} {
		if got := storagekey.PrefixesConflict(tc.a, tc.b); got != tc.conflict {
			t.Errorf("PrefixesConflict(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.conflict)
		}
		if got := storagekey.PrefixesConflict(tc.b, tc.a); got != tc.conflict {
			t.Errorf("PrefixesConflict(%q, %q) = %v, want %v (symmetric)", tc.b, tc.a, got, tc.conflict)
		}
	}
}

func TestPrefixKey(t *testing.T) {
	want := sha256.Sum256([]byte("tenants/a/projects/b"))
	for _, p := range []string{"tenants/a/projects/b", "Tenants/A/Projects/B", "TENANTS/A/PROJECTS/B"} {
		if got := storagekey.PrefixKey(p); got != hex.EncodeToString(want[:]) {
			t.Errorf("PrefixKey(%q) = %s", p, got)
		}
	}
	if storagekey.PrefixKey("a") == storagekey.PrefixKey("b") || len(storagekey.PrefixKey("a")) != 64 {
		t.Error("PrefixKey must be the 64-digit SHA-256 of the lowercased prefix")
	}
}

func TestDefaultPrefix(t *testing.T) {
	const tn, pr, ws = "01JD00000000000000000000T0", "01JD00000000000000000000P0", "01JD00000000000000000000W0"
	p, err := storagekey.DefaultPrefix(tn, pr, ws)
	if err != nil || p != "tenants/"+tn+"/projects/"+pr+"/workspaces/"+ws {
		t.Fatalf("DefaultPrefix = %q, %v", p, err)
	}
	if got := storagekey.Join(p, "objects/aa/bb"); got != p+"/objects/aa/bb" {
		t.Errorf("Join = %s", got)
	}
	for _, bad := range [][3]string{
		{"", pr, ws}, {"a/b", pr, ws}, {tn, "..", ws}, {tn, pr, "café"},
		{strings.ToLower(tn), pr, ws}, {tn, pr, ws[:25]}, {tn, pr, ws + "0"}, {tn, pr, "8" + ws[1:]},
	} {
		if _, err := storagekey.DefaultPrefix(bad[0], bad[1], bad[2]); !errors.Is(err, storagekey.ErrInvalidPrefix) {
			t.Errorf("DefaultPrefix(%q) err = %v", bad, err)
		}
	}
}

// TestDefaultPrefixesSatisfyTheRules: default prefixes built from ULIDs are
// valid, and the prefixes of different workspaces never conflict.
func TestDefaultPrefixesSatisfyTheRules(t *testing.T) {
	g := ids.NewGenerator()
	// The local ULID check agrees with internal/ids.
	for _, id := range []string{g.NewID(), "01JD00000000000000000000I0", "7ZZZZZZZZZZZZZZZZZZZZZZZZZ", "80000000000000000000000000"} {
		_, err := storagekey.DefaultPrefix(id, id, id)
		if (err == nil) != ids.Valid(id) {
			t.Errorf("%s: DefaultPrefix err %v, ids.Valid %v", id, err, ids.Valid(id))
		}
	}
	tenant, project := g.NewID(), g.NewID()
	var prefixes []string
	for i := 0; i < 200; i++ {
		tn, pr := tenant, project
		if i%3 == 1 {
			tn = g.NewID()
		}
		if i%3 == 2 {
			pr = g.NewID()
		}
		p, err := storagekey.DefaultPrefix(tn, pr, g.NewID())
		if err != nil {
			t.Fatal(err)
		}
		if err := storagekey.ValidatePrefix(p); err != nil {
			t.Fatal(err)
		}
		if len(p)+1+len(storagekey.TreeKey(contenthash.SumBytes(nil))) > storagekey.MaxKeyBytes {
			t.Fatalf("default prefix %q is too long", p)
		}
		prefixes = append(prefixes, p)
	}
	for i := range prefixes {
		for j := i + 1; j < len(prefixes); j++ {
			if storagekey.PrefixesConflict(prefixes[i], prefixes[j]) {
				t.Fatalf("default prefixes conflict: %q %q", prefixes[i], prefixes[j])
			}
		}
	}
}
