package tree

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/LUOSENGWA/MFGraph/internal/contenthash"
)

// DiffKind says how a file differs between two trees.
type DiffKind string

const (
	DiffAdded    DiffKind = "added"    // only in the target tree
	DiffModified DiffKind = "modified" // in both, with another hash or mode
	DiffDeleted  DiffKind = "deleted"  // only in the base tree
)

// DiffEntry is one changed file. Old is the file in the base tree (nil for
// added files), New the file in the target tree (nil for deleted files).
type DiffEntry struct {
	Path string // stored path, '/'-separated
	Kind DiffKind
	Old  *Entry
	New  *Entry
}

var errDiffLimit = errors.New("diff limit reached")

// Diff lists the files that differ from the base tree a to the target tree
// b, in path order (segment by segment, each segment by its bytes; a path
// sorts before the paths below it), starting after the path after (stored
// segments; nil = from the start), at most limit entries. A zero a or b, or
// EmptyRoot, is the empty tree. Subtrees with the same hash are skipped
// without reading them, and in paged directories pairs of identical pages
// are skipped as a whole. A file whose hash or mode changed is modified; a
// name that is a file in one tree and a directory in the other gives the
// deleted (or added) file first and then the files below the directory.
// more reports whether more entries follow.
func Diff(ctx context.Context, g Getter, a, b contenthash.Hash, after []string, limit int) (entries []DiffEntry, more bool, err error) {
	if limit < 1 {
		return nil, false, fmt.Errorf("tree: Diff limit %d", limit)
	}
	if len(after) > MaxDepth {
		return nil, false, fmt.Errorf("%w: cursor of %d segments", ErrTooDeep, len(after))
	}
	if a == EmptyRoot {
		a = contenthash.Hash{}
	}
	if b == EmptyRoot {
		b = contenthash.Hash{}
	}
	d := &differ{r: newReader(ctx, g), limit: limit}
	if a != b {
		err = d.dir(nil, a, b, after, 0)
	}
	switch {
	case errors.Is(err, errDiffLimit):
		return d.out[:limit], true, nil
	case err != nil:
		return nil, false, err
	}
	return d.out, false, nil
}

type differ struct {
	r     *reader
	limit int
	out   []DiffEntry
}

func (d *differ) emit(path []string, name string, kind DiffKind, old, new *Entry) error {
	d.out = append(d.out, DiffEntry{Path: joinSegs(path, name), Kind: kind, Old: old, New: new})
	if len(d.out) > d.limit {
		return errDiffLimit
	}
	return nil
}

func joinSegs(dir []string, name string) string {
	if len(dir) == 0 {
		return name
	}
	return strings.Join(dir, "/") + "/" + name
}

// dir compares the directories a and b (zero = absent) at path. after is
// the rest of the resume position below this directory (nil: no
// constraint).
func (d *differ) dir(path []string, a, b contenthash.Hash, after []string, depth int) error {
	if depth > MaxDepth {
		return fmt.Errorf("%w: more than %d levels", ErrTooDeep, MaxDepth)
	}
	from := ""
	if len(after) > 0 {
		from = after[0]
	}
	ia, err := openDir(d.r, a, from)
	if err != nil {
		return err
	}
	ib, err := openDir(d.r, b, from)
	if err != nil {
		return err
	}
	for {
		// Identical pages (or index subtrees) at the same position hold
		// identical entries; they are skipped without reading them. (A
		// reference start is at or after the resume position, so what
		// follows it is after it.)
		if ha, ok := ia.refStart(); ok {
			if hb, ok := ib.refStart(); ok && ha == hb {
				ia.skipRef()
				ib.skipRef()
				after = nil
				continue
			}
		}
		ea, okA, err := ia.peek()
		if err != nil {
			return err
		}
		eb, okB, err := ib.peek()
		if err != nil {
			return err
		}
		if !okA && !okB {
			return nil
		}
		var name string
		switch {
		case !okB || okA && ea.Name < eb.Name:
			name, okB = ea.Name, false
		case !okA || eb.Name < ea.Name:
			name, okA = eb.Name, false
		default:
			name = ea.Name
		}
		// The resume position: the file at the cursor itself has been
		// listed; what is below it has not (if after has one segment) or
		// only partly (more segments).
		skipFile, sub := false, []string(nil)
		if after != nil && name == after[0] {
			skipFile, sub = true, after[1:]
			if len(sub) == 0 {
				sub = nil
			}
		}
		if okA && okB && ea.Type == eb.Type && ea.Hash == eb.Hash && ea.Mode == eb.Mode {
			// Unchanged file or subtree.
		} else {
			var fa, fb *Entry
			var da, db contenthash.Hash
			if okA {
				if ea.Type == TypeFile {
					fa = &ea
				} else {
					da = ea.Hash
				}
			}
			if okB {
				if eb.Type == TypeFile {
					fb = &eb
				} else {
					db = eb.Hash
				}
			}
			if !skipFile {
				var err error
				switch {
				case fa != nil && fb != nil:
					err = d.emit(path, name, DiffModified, fa, fb)
				case fa != nil:
					err = d.emit(path, name, DiffDeleted, fa, nil)
				case fb != nil:
					err = d.emit(path, name, DiffAdded, nil, fb)
				}
				if err != nil {
					return err
				}
			}
			if (!da.IsZero() || !db.IsZero()) && da != db {
				if err := d.dir(append(path[:len(path):len(path)], name), da, db, sub, depth+1); err != nil {
					return err
				}
			}
		}
		after = nil
		if okA {
			if err := ia.next(); err != nil {
				return err
			}
		}
		if okB {
			if err := ib.next(); err != nil {
				return err
			}
		}
	}
}
