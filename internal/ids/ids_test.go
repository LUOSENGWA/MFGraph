package ids_test

import (
	"sync"
	"testing"

	"github.com/LUOSENGWA/MFGraph/internal/ids"
)

func TestGenerator(t *testing.T) {
	g := ids.NewGenerator()
	seen := map[string]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				id := g.NewID()
				if !ids.Valid(id) {
					t.Errorf("invalid ULID %q", id)
				}
				mu.Lock()
				if seen[id] {
					t.Errorf("duplicate %q", id)
				}
				seen[id] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	a, b := g.NewID(), g.NewID()
	if !(a < b) {
		t.Fatalf("not monotonic: %s >= %s", a, b)
	}
}

func TestValid(t *testing.T) {
	for _, s := range []string{"01JD3X5Q8Z9W2V4T6R8P0N2M4K", "00000000000000000000000000", "7ZZZZZZZZZZZZZZZZZZZZZZZZZ"} {
		if !ids.Valid(s) {
			t.Errorf("Valid(%q) = false", s)
		}
	}
	for _, s := range []string{"", "01jd3x5q8z9w2v4t6r8p0n2m4k", "8ZZZZZZZZZZZZZZZZZZZZZZZZZ", "01JD3X5Q8Z9W2V4T6R8P0N2M4", "01JD3X5Q8Z9W2V4T6R8P0N2M4KX", "01JD3X5Q8Z9W2V4T6R8P0N2M4I", "01JD3X5Q8Z9W2V4T6R8P0N2M4U"} {
		if ids.Valid(s) {
			t.Errorf("Valid(%q) = true", s)
		}
	}
}
