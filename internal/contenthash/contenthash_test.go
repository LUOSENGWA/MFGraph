package contenthash_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
)

type vectorFile struct {
	Schema       string            `json:"schema"`
	ReviewStatus string            `json:"review_status"`
	Description  string            `json:"description"`
	Inputs       map[string]string `json:"inputs"`
	Vectors      []struct {
		Name  string `json:"name"`
		Input struct {
			UTF8      *string `json:"utf8"`
			Hex       *string `json:"hex"`
			Generator string  `json:"generator"`
			Seed      string  `json:"seed"`
			Length    int     `json:"length"`
		} `json:"input"`
		Size int64  `json:"size"`
		Hash string `json:"hash"`
	} `json:"vectors"`
	Parse struct {
		Valid   []string `json:"valid"`
		Invalid []struct {
			Input  string `json:"input"`
			Reason string `json:"reason"`
		} `json:"invalid"`
	} `json:"parse"`
}

func loadVectors(t *testing.T) vectorFile {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "contenthash.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v vectorFile
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode vectors: %v", err)
	}
	if len(v.Vectors) == 0 {
		t.Fatal("no vectors")
	}
	return v
}

// sha256Counter regenerates the "sha256-counter" generator input. It is a
// test-side re-implementation of the rule documented in the vector file.
func sha256Counter(seed string, length int) []byte {
	out := make([]byte, 0, length+sha256.Size)
	var ctr [4]byte
	for i := uint32(0); len(out) < length; i++ {
		binary.BigEndian.PutUint32(ctr[:], i)
		block := sha256.Sum256(append([]byte(seed), ctr[:]...))
		out = append(out, block[:]...)
	}
	return out[:length]
}

func TestVectors(t *testing.T) {
	v := loadVectors(t)
	for _, tc := range v.Vectors {
		t.Run(tc.Name, func(t *testing.T) {
			var input []byte
			switch {
			case tc.Input.UTF8 != nil:
				input = []byte(*tc.Input.UTF8)
			case tc.Input.Hex != nil:
				var err error
				if input, err = hex.DecodeString(*tc.Input.Hex); err != nil {
					t.Fatal(err)
				}
			case tc.Input.Generator == "sha256-counter":
				input = sha256Counter(tc.Input.Seed, tc.Input.Length)
			default:
				t.Fatalf("unknown input kind")
			}
			// Streaming through a reader that returns one byte at a time must
			// give the same result as hashing the slice.
			h, n, err := contenthash.Sum(iotest.OneByteReader(bytes.NewReader(input)))
			if err != nil {
				t.Fatal(err)
			}
			if n != tc.Size || int64(len(input)) != tc.Size {
				t.Errorf("size = %d (input %d), want %d", n, len(input), tc.Size)
			}
			if got := h.String(); got != tc.Hash {
				t.Errorf("Sum = %s, want %s", got, tc.Hash)
			}
			if got := contenthash.SumBytes(input).String(); got != tc.Hash {
				t.Errorf("SumBytes = %s, want %s", got, tc.Hash)
			}
			want := contenthash.MustParse(tc.Hash)
			if h != want {
				t.Errorf("parsed hash differs from computed hash")
			}
			if h.Hex() != strings.TrimPrefix(tc.Hash, "sha256:") || h.Algorithm() != "sha256" {
				t.Errorf("Hex/Algorithm mismatch: %s %s", h.Hex(), h.Algorithm())
			}
		})
	}
}

func TestParseVectors(t *testing.T) {
	v := loadVectors(t)
	for _, s := range v.Parse.Valid {
		h, err := contenthash.Parse(s)
		if err != nil {
			t.Errorf("Parse(%q): %v", s, err)
			continue
		}
		if h.String() != s {
			t.Errorf("round trip %q -> %q", s, h.String())
		}
	}
	for _, tc := range v.Parse.Invalid {
		if _, err := contenthash.Parse(tc.Input); !errors.Is(err, contenthash.ErrInvalid) {
			t.Errorf("Parse(%q) [%s]: err = %v, want ErrInvalid", tc.Input, tc.Reason, err)
		}
	}
}

func TestParseHex(t *testing.T) {
	const hx = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	h, err := contenthash.ParseHex("sha256", hx)
	if err != nil || h.Hex() != hx {
		t.Fatalf("ParseHex: %v %s", err, h.Hex())
	}
	for _, bad := range []struct{ algo, hex string }{
		{"SHA256", hx}, {"sha512", hx}, {"", hx}, {"sha256", strings.ToUpper(hx)}, {"sha256", "sha256:" + hx}, {"sha256", hx[:63]},
	} {
		if _, err := contenthash.ParseHex(bad.algo, bad.hex); !errors.Is(err, contenthash.ErrInvalid) {
			t.Errorf("ParseHex(%q, %q): err = %v", bad.algo, bad.hex, err)
		}
	}
}

func TestZeroValue(t *testing.T) {
	var h contenthash.Hash
	if !h.IsZero() || h.String() != "" || h.Hex() != "" {
		t.Fatalf("zero value: %q %q", h.String(), h.Hex())
	}
	if _, err := h.MarshalText(); !errors.Is(err, contenthash.ErrInvalid) {
		t.Fatalf("MarshalText(zero) err = %v", err)
	}
	if contenthash.SumBytes(nil).IsZero() {
		t.Fatal("hash of empty input must not be the zero value")
	}
}

func TestTextMarshaling(t *testing.T) {
	type doc struct {
		H contenthash.Hash `json:"h"`
	}
	in := doc{H: contenthash.SumBytes([]byte("hello"))}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"h":"sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"}` {
		t.Fatalf("marshal: %s", b)
	}
	var out doc
	if err := json.Unmarshal(b, &out); err != nil || out != in {
		t.Fatalf("unmarshal: %v %+v", err, out)
	}
	if err := json.Unmarshal([]byte(`{"h":"SHA256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"}`), &out); err == nil {
		t.Fatal("unmarshal accepted uppercase prefix")
	}
}

func TestHasher(t *testing.T) {
	x := contenthash.NewHasher()
	_, _ = x.Write([]byte("hel"))
	_, _ = x.Write([]byte("lo"))
	if x.Size() != 5 || x.Sum() != contenthash.SumBytes([]byte("hello")) {
		t.Fatalf("Hasher: size %d hash %s", x.Size(), x.Sum())
	}
}

func TestSumReaderError(t *testing.T) {
	boom := errors.New("boom")
	if _, _, err := contenthash.Sum(iotest.ErrReader(boom)); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}
