package storagekey_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
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

func TestDefaultPrefix(t *testing.T) {
	const tn, pr, ws = "01JD00000000000000000000T0", "01JD00000000000000000000P0", "01JD00000000000000000000W0"
	p, err := storagekey.DefaultPrefix(tn, pr, ws)
	if err != nil || p != "tenants/"+tn+"/projects/"+pr+"/workspaces/"+ws {
		t.Fatalf("DefaultPrefix = %q, %v", p, err)
	}
	if got := storagekey.Join(p, "objects/aa/bb"); got != p+"/objects/aa/bb" {
		t.Errorf("Join = %s", got)
	}
	for _, bad := range [][3]string{{"", pr, ws}, {"a/b", pr, ws}, {tn, "..", ws}, {tn, pr, "café"}} {
		if _, err := storagekey.DefaultPrefix(bad[0], bad[1], bad[2]); !errors.Is(err, storagekey.ErrInvalidPrefix) {
			t.Errorf("DefaultPrefix(%q) err = %v", bad, err)
		}
	}
}

func TestValidatePrefix(t *testing.T) {
	ok := []string{"projects/01JD00000000000000000000P0", "a", "tenants/t/projects/p/workspaces/w", "文档/空间"}
	for _, p := range ok {
		if err := storagekey.ValidatePrefix(p); err != nil {
			t.Errorf("ValidatePrefix(%q) = %v", p, err)
		}
	}
	bad := []string{"", "/a", "a/", "a//b", "a/../b", "a\\b", "a/\x00", "café", strings.Repeat("a/", 512) + "b"}
	for _, p := range bad {
		if err := storagekey.ValidatePrefix(p); !errors.Is(err, storagekey.ErrInvalidPrefix) {
			t.Errorf("ValidatePrefix(%q) = %v, want ErrInvalidPrefix", p, err)
		}
	}
}
