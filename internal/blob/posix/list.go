package posix

import (
	"context"
	"errors"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"io/fs"
	"path/filepath"
)

// Walk lists regular files in this directory without following symlinks.
func (s *Store) Walk(ctx context.Context, dir string, visit func(ports.ObjectInfo) error) error {
	root, err := s.path(dir)
	if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() || p == root {
			return nil
		}
		i, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		key, err := filepath.Rel(s.root, p)
		if err != nil {
			return err
		}
		return visit(ports.ObjectInfo{Key: filepath.ToSlash(key), Size: i.Size(), ModTime: i.ModTime()})
	})
}
