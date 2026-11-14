package tree

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
	"github.com/LUOSENGWA/MFGraph/internal/pathx"
)

const (
	// FormatVersion is the "format" value of every node defined here.
	FormatVersion = 1

	// MaxEntriesPerNode bounds the entries of a dir/dir_page node and the
	// references of a dir_index node. Frozen; not configurable.
	MaxEntriesPerNode = 1024

	// MaxNodeBytes bounds the canonical encoding of any node (1 MiB). Frozen.
	MaxNodeBytes = 1 << 20

	// ModeRegular (0644) and ModeExecutable (0755) are the only file modes.
	ModeRegular    uint32 = 0o644 // 420
	ModeExecutable uint32 = 0o755 // 493

	// MaxSafeInteger is the largest integer written into a node (2^53-1), so
	// that every RFC 8785 implementation reproduces the bytes exactly.
	MaxSafeInteger = 1<<53 - 1
)

// Kind is the "kind" of a node.
type Kind string

const (
	KindDir      Kind = "dir"       // a whole directory
	KindDirPage  Kind = "dir_page"  // one page of a large directory
	KindDirIndex Kind = "dir_index" // index over pages or lower-level indexes
)

// EntryType is the "type" of a directory entry. There is no symlink type.
type EntryType string

const (
	TypeFile EntryType = "file"
	TypeDir  EntryType = "dir"
)

// Entry is one name in a directory. Size and Mode are meaningful for files
// only; for a directory Hash is the hash of its top node (dir or dir_index).
type Entry struct {
	Name string
	Type EntryType
	Hash contenthash.Hash
	Size int64
	Mode uint32
}

// PageRef is one reference of a dir_index node.
type PageRef struct {
	// Count is the number of directory entries covered by the referenced
	// node (for a lower-level dir_index: the sum over its references).
	Count int64
	// First is the name of the first entry covered by the referenced node.
	First string
	// Hash is the hash of the referenced dir_page or dir_index node.
	Hash contenthash.Hash
}

// Node is a decoded tree node. Entries is used by dir and dir_page, Pages by
// dir_index.
type Node struct {
	Kind    Kind
	Entries []Entry
	Pages   []PageRef
}

var (
	// ErrInvalidNode: the bytes are not a structurally valid format-1 node.
	ErrInvalidNode = errors.New("tree: invalid node")
	// ErrNonCanonical: the node decodes but its bytes are not the canonical
	// encoding (whitespace, key order, escapes, number syntax, ...).
	ErrNonCanonical = errors.New("tree: non-canonical node encoding")
	// ErrUnsupportedFormat: the node declares a format version other than 1.
	ErrUnsupportedFormat = errors.New("tree: unsupported node format")
	// ErrHashMismatch: node bytes do not hash to the expected value.
	ErrHashMismatch = errors.New("tree: node hash mismatch")
	// ErrInvalidTree: nodes are individually valid but do not form a valid
	// tree (empty subdirectory, wrong page counts, non-canonical paging, ...).
	ErrInvalidTree = errors.New("tree: invalid tree")
	// ErrInvalidEntry: an input file of Build is invalid (hash, size, mode).
	ErrInvalidEntry = errors.New("tree: invalid file entry")
	// ErrNodeNotFound: a Getter has no node for the requested hash.
	ErrNodeNotFound = errors.New("tree: node not found")
)

// HashOf returns the node hash: SHA-256 of the uncompressed canonical bytes.
func HashOf(canonical []byte) contenthash.Hash { return contenthash.SumBytes(canonical) }

// Encode returns the canonical encoding of n: RFC 8785 (JCS) JSON with the
// key order fixed by the format, no whitespace, strings escaping only '"',
// '\' and U+0000-U+001F, integers in plain decimal. Entries and pages are
// written in the order given; Build and Decode guarantee they are sorted.
// Encode does not validate n; see Validate.
func Encode(n Node) []byte {
	return appendNode(make([]byte, 0, 64+len(n.Entries)*128+len(n.Pages)*96), n)
}

func appendNode(b []byte, n Node) []byte {
	if n.Kind == KindDirIndex {
		b = append(b, `{"format":1,"kind":"dir_index","pages":[`...)
		for i, p := range n.Pages {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendPageRef(b, p)
		}
		return append(b, "]}"...)
	}
	b = append(b, `{"entries":[`...)
	for i, e := range n.Entries {
		if i > 0 {
			b = append(b, ',')
		}
		b = appendEntry(b, e)
	}
	b = append(b, `],"format":1,"kind":`...)
	b = appendString(b, string(n.Kind))
	return append(b, '}')
}

func appendEntry(b []byte, e Entry) []byte {
	b = append(b, `{"hash":"`...)
	b = append(b, e.Hash.String()...)
	if e.Type == TypeFile {
		b = append(b, `","mode":`...)
		b = strconv.AppendUint(b, uint64(e.Mode), 10)
		b = append(b, `,"name":`...)
		b = appendString(b, e.Name)
		b = append(b, `,"size":`...)
		b = strconv.AppendInt(b, e.Size, 10)
	} else {
		b = append(b, `","name":`...)
		b = appendString(b, e.Name)
	}
	b = append(b, `,"type":`...)
	b = appendString(b, string(e.Type))
	return append(b, '}')
}

func appendPageRef(b []byte, p PageRef) []byte {
	b = append(b, `{"count":`...)
	b = strconv.AppendInt(b, p.Count, 10)
	b = append(b, `,"first":`...)
	b = appendString(b, p.First)
	b = append(b, `,"hash":"`...)
	b = append(b, p.Hash.String()...)
	return append(b, `"}`...)
}

const hexDigits = "0123456789abcdef"

// appendString writes s as a JCS string. s is assumed to be valid UTF-8; all
// bytes >= 0x20 other than '"' and '\' are copied unchanged.
func appendString(b []byte, s string) []byte {
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			b = append(b, '\\', '"')
		case c == '\\':
			b = append(b, '\\', '\\')
		case c >= 0x20:
			b = append(b, c)
		case c == '\b':
			b = append(b, '\\', 'b')
		case c == '\t':
			b = append(b, '\\', 't')
		case c == '\n':
			b = append(b, '\\', 'n')
		case c == '\f':
			b = append(b, '\\', 'f')
		case c == '\r':
			b = append(b, '\\', 'r')
		default:
			b = append(b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
		}
	}
	return append(b, '"')
}

// Validate checks the read-time rules of a single node: kind, entry and
// reference fields, names (pathx.ValidateStoredName: structural rules only),
// strict byte order of names (which also excludes a name used twice or as
// both file and directory), reference counts of at least 1, and the entry
// limits. It does not check NFC, case-fold conflicts, unassigned code points
// or the fixed disallowed list: those are write-time rules (enforced by
// Build), and stored trees must stay readable when they change.
func (n Node) Validate() error {
	switch n.Kind {
	case KindDir, KindDirPage:
		if n.Pages != nil {
			return fmt.Errorf("%w: %s node has pages", ErrInvalidNode, n.Kind)
		}
		if n.Kind == KindDirPage && len(n.Entries) == 0 {
			return fmt.Errorf("%w: empty dir_page", ErrInvalidNode)
		}
		if len(n.Entries) > MaxEntriesPerNode {
			return fmt.Errorf("%w: %d entries exceed %d", ErrInvalidNode, len(n.Entries), MaxEntriesPerNode)
		}
		for i, e := range n.Entries {
			if err := validateEntry(e); err != nil {
				return err
			}
			if i > 0 && n.Entries[i-1].Name >= e.Name {
				return fmt.Errorf("%w: entries not in strictly ascending byte order at %q", ErrInvalidNode, e.Name)
			}
		}
	case KindDirIndex:
		if n.Entries != nil {
			return fmt.Errorf("%w: dir_index node has entries", ErrInvalidNode)
		}
		if len(n.Pages) == 0 || len(n.Pages) > MaxEntriesPerNode {
			return fmt.Errorf("%w: dir_index has %d references", ErrInvalidNode, len(n.Pages))
		}
		for i, p := range n.Pages {
			if p.Count < 1 || p.Count > MaxSafeInteger {
				return fmt.Errorf("%w: reference count %d out of range", ErrInvalidNode, p.Count)
			}
			if err := pathx.ValidateStoredName(p.First); err != nil {
				return fmt.Errorf("%w: reference first name: %v", ErrInvalidNode, err)
			}
			if p.Hash.IsZero() {
				return fmt.Errorf("%w: reference without hash", ErrInvalidNode)
			}
			if i > 0 && n.Pages[i-1].First >= p.First {
				return fmt.Errorf("%w: references not in strictly ascending order at %q", ErrInvalidNode, p.First)
			}
		}
	default:
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidNode, n.Kind)
	}
	return nil
}

func validateEntry(e Entry) error {
	if err := pathx.ValidateStoredName(e.Name); err != nil {
		return fmt.Errorf("%w: entry name: %v", ErrInvalidNode, err)
	}
	if e.Hash.IsZero() {
		return fmt.Errorf("%w: entry %q without hash", ErrInvalidNode, e.Name)
	}
	switch e.Type {
	case TypeFile:
		if e.Mode != ModeRegular && e.Mode != ModeExecutable {
			return fmt.Errorf("%w: entry %q: mode %d is not 420 or 493", ErrInvalidNode, e.Name, e.Mode)
		}
		if e.Size < 0 || e.Size > MaxSafeInteger {
			return fmt.Errorf("%w: entry %q: size %d out of range", ErrInvalidNode, e.Name, e.Size)
		}
	case TypeDir:
		if e.Mode != 0 || e.Size != 0 {
			return fmt.Errorf("%w: directory entry %q has mode or size", ErrInvalidNode, e.Name)
		}
	default:
		return fmt.Errorf("%w: entry %q: unknown type %q", ErrInvalidNode, e.Name, e.Type)
	}
	return nil
}

// Wire structs: pointers distinguish absent from zero values.
type rawNode struct {
	Entries *[]rawEntry `json:"entries"`
	Format  *int64      `json:"format"`
	Kind    *string     `json:"kind"`
	Pages   *[]rawPage  `json:"pages"`
}

type rawEntry struct {
	Hash *string `json:"hash"`
	Mode *uint32 `json:"mode"`
	Name *string `json:"name"`
	Size *int64  `json:"size"`
	Type *string `json:"type"`
}

type rawPage struct {
	Count *int64  `json:"count"`
	First *string `json:"first"`
	Hash  *string `json:"hash"`
}

// Decode parses and validates one node strictly (read-time rules): the node
// must satisfy Validate and its bytes must be exactly Encode of the decoded
// node, so every node has a single accepted byte representation.
func Decode(b []byte) (Node, error) {
	if len(b) > MaxNodeBytes {
		return Node{}, fmt.Errorf("%w: %d bytes exceed %d", ErrInvalidNode, len(b), MaxNodeBytes)
	}
	var raw rawNode
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return Node{}, fmt.Errorf("%w: %v", ErrInvalidNode, err)
	}
	if raw.Format == nil || raw.Kind == nil {
		return Node{}, fmt.Errorf("%w: missing format or kind", ErrInvalidNode)
	}
	if *raw.Format != FormatVersion {
		return Node{}, fmt.Errorf("%w: format %d", ErrUnsupportedFormat, *raw.Format)
	}
	n := Node{Kind: Kind(*raw.Kind)}
	switch n.Kind {
	case KindDir, KindDirPage:
		if raw.Entries == nil || raw.Pages != nil {
			return Node{}, fmt.Errorf("%w: %s node needs entries and no pages", ErrInvalidNode, n.Kind)
		}
		n.Entries = make([]Entry, 0, len(*raw.Entries))
		for _, re := range *raw.Entries {
			e, err := decodeEntry(re)
			if err != nil {
				return Node{}, err
			}
			n.Entries = append(n.Entries, e)
		}
	case KindDirIndex:
		if raw.Pages == nil || raw.Entries != nil {
			return Node{}, fmt.Errorf("%w: dir_index node needs pages and no entries", ErrInvalidNode)
		}
		n.Pages = make([]PageRef, 0, len(*raw.Pages))
		for _, rp := range *raw.Pages {
			if rp.Count == nil || rp.First == nil || rp.Hash == nil {
				return Node{}, fmt.Errorf("%w: incomplete page reference", ErrInvalidNode)
			}
			h, err := contenthash.Parse(*rp.Hash)
			if err != nil {
				return Node{}, fmt.Errorf("%w: %v", ErrInvalidNode, err)
			}
			n.Pages = append(n.Pages, PageRef{Count: *rp.Count, First: *rp.First, Hash: h})
		}
	default:
		return Node{}, fmt.Errorf("%w: unknown kind %q", ErrInvalidNode, n.Kind)
	}
	if err := n.Validate(); err != nil {
		return Node{}, err
	}
	if !bytes.Equal(Encode(n), b) {
		return Node{}, ErrNonCanonical
	}
	return n, nil
}

func decodeEntry(re rawEntry) (Entry, error) {
	if re.Name == nil || re.Type == nil || re.Hash == nil {
		return Entry{}, fmt.Errorf("%w: entry needs hash, name and type", ErrInvalidNode)
	}
	h, err := contenthash.Parse(*re.Hash)
	if err != nil {
		return Entry{}, fmt.Errorf("%w: %v", ErrInvalidNode, err)
	}
	e := Entry{Name: *re.Name, Type: EntryType(*re.Type), Hash: h}
	switch e.Type {
	case TypeFile:
		if re.Mode == nil || re.Size == nil {
			return Entry{}, fmt.Errorf("%w: file entry %q needs mode and size", ErrInvalidNode, e.Name)
		}
		e.Mode, e.Size = *re.Mode, *re.Size
	case TypeDir:
		if re.Mode != nil || re.Size != nil {
			return Entry{}, fmt.Errorf("%w: directory entry %q has mode or size", ErrInvalidNode, e.Name)
		}
	}
	return e, nil
}

// DecodeVerified checks that b hashes to want and then decodes it strictly.
func DecodeVerified(b []byte, want contenthash.Hash) (Node, error) {
	if got := HashOf(b); got != want {
		return Node{}, fmt.Errorf("%w: want %s, got %s", ErrHashMismatch, want, got)
	}
	return Decode(b)
}
