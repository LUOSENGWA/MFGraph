// Package ids generates and validates ULIDs: 26 uppercase Crockford base32
// characters, time-ordered.
package ids

import (
	"crypto/rand"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// Generator produces ULIDs with crypto/rand entropy, monotonic within the
// same millisecond. It is safe for concurrent use.
type Generator struct {
	mu      sync.Mutex
	entropy *ulid.MonotonicEntropy
	now     func() time.Time
}

// NewGenerator returns a Generator using the system clock.
func NewGenerator() *Generator {
	return &Generator{entropy: ulid.Monotonic(rand.Reader, 0), now: time.Now}
}

// NewID returns a new ULID string.
func (g *Generator) NewID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return ulid.MustNew(ulid.Timestamp(g.now()), g.entropy).String()
}

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// Valid reports whether s is a canonical ULID: exactly 26 characters from the
// uppercase Crockford base32 alphabet, first character 0-7 (no overflow).
func Valid(s string) bool {
	if len(s) != ulid.EncodedSize || s[0] > '7' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isCrockford(s[i]) {
			return false
		}
	}
	return true
}

func isCrockford(c byte) bool {
	for i := 0; i < len(crockford); i++ {
		if crockford[i] == c {
			return true
		}
	}
	return false
}
