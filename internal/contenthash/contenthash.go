package contenthash

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
)

const (
	// AlgoSHA256 is the only content hash algorithm of format version 1. It is
	// also the value of the hash_algo database column.
	AlgoSHA256 = "sha256"

	// DigestSize is the length of a SHA-256 digest in bytes.
	DigestSize = sha256.Size

	// HexLen is the length of the lowercase hex form of a digest.
	HexLen = 2 * DigestSize

	prefix = AlgoSHA256 + ":"
)

// ErrInvalid is returned (wrapped) for any malformed textual hash.
var ErrInvalid = errors.New("contenthash: invalid hash")

// Hash is a content hash: an algorithm plus a 32-byte digest. The zero value
// is "no hash" and is rejected wherever a hash is required. Hash is
// comparable and can be used as a map key.
type Hash struct {
	algo   string
	digest [DigestSize]byte
}

// FromDigest returns the SHA-256 hash with the given digest.
func FromDigest(d [DigestSize]byte) Hash {
	return Hash{algo: AlgoSHA256, digest: d}
}

// Sum streams r and returns its SHA-256 hash and the number of bytes read.
func Sum(r io.Reader) (Hash, int64, error) {
	h := NewHasher()
	n, err := io.Copy(h, r)
	if err != nil {
		return Hash{}, n, err
	}
	return h.Sum(), n, nil
}

// SumBytes returns the SHA-256 hash of b.
func SumBytes(b []byte) Hash {
	return FromDigest(sha256.Sum256(b))
}

// Parse parses the canonical textual form "sha256:" followed by exactly 64
// lowercase hex digits. Anything else (uppercase digits, other prefixes,
// surrounding whitespace, wrong length) is rejected so that every hash has a
// single textual representation.
func Parse(s string) (Hash, error) {
	if len(s) != len(prefix)+HexLen || s[:len(prefix)] != prefix {
		return Hash{}, fmt.Errorf("%w: want %q followed by %d lowercase hex digits", ErrInvalid, prefix, HexLen)
	}
	return parseHex(s[len(prefix):])
}

// MustParse is like Parse but panics on error. Intended for constants in tests.
func MustParse(s string) Hash {
	h, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return h
}

// ParseHex parses the database form: an algorithm name (the hash_algo column)
// and a 64-digit lowercase hex digest without prefix.
func ParseHex(algo, hexDigest string) (Hash, error) {
	if algo != AlgoSHA256 {
		return Hash{}, fmt.Errorf("%w: unsupported algorithm %q", ErrInvalid, algo)
	}
	return parseHex(hexDigest)
}

func parseHex(s string) (Hash, error) {
	if len(s) != HexLen {
		return Hash{}, fmt.Errorf("%w: digest must be %d hex digits, got %d", ErrInvalid, HexLen, len(s))
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return Hash{}, fmt.Errorf("%w: digest must be lowercase hex", ErrInvalid)
		}
	}
	var d [DigestSize]byte
	if _, err := hex.Decode(d[:], []byte(s)); err != nil {
		return Hash{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return FromDigest(d), nil
}

// IsZero reports whether h is the zero value.
func (h Hash) IsZero() bool { return h.algo == "" }

// Algorithm returns the algorithm name ("sha256"), or "" for the zero value.
func (h Hash) Algorithm() string { return h.algo }

// Digest returns the raw digest bytes.
func (h Hash) Digest() [DigestSize]byte { return h.digest }

// Hex returns the 64-digit lowercase hex digest without prefix (the database
// form), or "" for the zero value.
func (h Hash) Hex() string {
	if h.IsZero() {
		return ""
	}
	return hex.EncodeToString(h.digest[:])
}

// String returns the canonical form "sha256:<hex>", or "" for the zero value.
func (h Hash) String() string {
	if h.IsZero() {
		return ""
	}
	return prefix + h.Hex()
}

// MarshalText implements encoding.TextMarshaler. The zero value is an error.
func (h Hash) MarshalText() ([]byte, error) {
	if h.IsZero() {
		return nil, fmt.Errorf("%w: zero hash", ErrInvalid)
	}
	return []byte(h.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler using the strict Parse.
func (h *Hash) UnmarshalText(b []byte) error {
	v, err := Parse(string(b))
	if err != nil {
		return err
	}
	*h = v
	return nil
}

// Hasher computes a Hash and a byte count incrementally. It is an io.Writer.
type Hasher struct {
	h hash.Hash
	n int64
}

// NewHasher returns a Hasher for SHA-256.
func NewHasher() *Hasher { return &Hasher{h: sha256.New()} }

// Write adds p to the running hash. It never returns an error.
func (x *Hasher) Write(p []byte) (int, error) {
	n, _ := x.h.Write(p)
	x.n += int64(n)
	return n, nil
}

// Sum returns the hash of everything written so far.
func (x *Hasher) Sum() Hash {
	var d [DigestSize]byte
	copy(d[:], x.h.Sum(nil))
	return FromDigest(d)
}

// Size returns the number of bytes written so far.
func (x *Hasher) Size() int64 { return x.n }
