package memory

import (
	"context"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
	"sort"
)

type partKey struct {
	id     string
	number int
}

func (s *Store) InsertUpload(_ context.Context, tx ports.Tx, u ports.Upload) error {
	st, err := s.write(tx)
	if err != nil {
		return err
	}
	if _, found := st.uploads[u.ID]; found {
		return exists("upload")
	}
	st.uploads[u.ID] = u
	return nil
}

func (s *Store) GetUpload(_ context.Context, tx ports.Tx, id string) (ports.Upload, error) {
	st, release, err := s.read(tx)
	if err != nil {
		return ports.Upload{}, err
	}
	defer release()
	u, ok := st.uploads[id]
	if !ok {
		return u, ports.ErrNotFound
	}
	return u, nil
}

func (s *Store) UpdateUpload(_ context.Context, tx ports.Tx, u ports.Upload) error {
	st, err := s.write(tx)
	if err != nil {
		return err
	}
	if _, found := st.uploads[u.ID]; !found {
		return ports.ErrNotFound
	}
	st.uploads[u.ID] = u
	return nil
}

func (s *Store) SaveUploadPart(_ context.Context, tx ports.Tx, id string, p ports.Part, _ int64) error {
	st, err := s.write(tx)
	if err != nil {
		return err
	}
	st.parts[partKey{id, p.Number}] = p
	return nil
}

func (s *Store) UploadParts(_ context.Context, tx ports.Tx, id string) ([]ports.Part, error) {
	st, release, err := s.read(tx)
	if err != nil {
		return nil, err
	}
	defer release()
	var out []ports.Part
	for k, p := range st.parts {
		if k.id == id {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out, nil
}

func (s *Store) PendingUploadCleanup(_ context.Context, backend string, now int64, after string, limit int) ([]string, error) {
	st, release, err := s.read(nil)
	if err != nil {
		return nil, err
	}
	defer release()
	var ids []string
	for id, u := range st.uploads {
		if id > after && u.Backend == backend && u.Cleaned == 0 && (u.Expires <= now || u.Terminal()) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids[:min(limit, len(ids))], nil
}

func (s *Store) GetUsage(_ context.Context, tx ports.Tx, ws string) (ports.Usage, error) {
	st, release, err := s.read(tx)
	if err != nil {
		return ports.Usage{}, err
	}
	defer release()
	u, ok := st.usage[ws]
	if !ok {
		return u, ports.ErrNotFound
	}
	return u, nil
}
func (s *Store) SetUsage(_ context.Context, tx ports.Tx, ws string, u ports.Usage, _ int64) error {
	st, err := s.write(tx)
	if err != nil {
		return err
	}
	if _, ok := st.usage[ws]; !ok {
		return ports.ErrNotFound
	}
	st.usage[ws] = u
	return nil
}
