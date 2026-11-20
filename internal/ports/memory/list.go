package memory

import (
	"context"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"sort"
	"strings"
)

func (s *BlobStore) Walk(ctx context.Context, dir string, visit func(ports.ObjectInfo) error) error {
	s.mu.RLock()
	var objects []ports.ObjectInfo
	for key, o := range s.objects {
		if strings.HasPrefix(key, dir+"/") {
			objects = append(objects, ports.ObjectInfo{Key: key, Size: int64(len(o.data)), ModTime: o.modTime, Hash: o.hash})
		}
	}
	s.mu.RUnlock()
	sort.Slice(objects, func(i, j int) bool { return objects[i].Key < objects[j].Key })
	for _, i := range objects {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := visit(i); err != nil {
			return err
		}
	}
	return ctx.Err()
}
