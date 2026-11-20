package s3

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"

	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

var _ ports.MultipartStore = (*Store)(nil)

func (s *Store) StartMultipart(ctx context.Context, key string) (string, error) {
	if s.native.Load() < 0 {
		return "", ports.ErrUnsupportedMultipart
	}
	id, err := s.client.NewMultipartUpload(ctx, s.bucket, key, minio.PutObjectOptions{})
	if minio.ToErrorResponse(err).Code == "NotImplemented" {
		s.native.Store(-1)
		return "", ports.ErrUnsupportedMultipart
	}
	if err == nil {
		s.native.Store(1)
	}
	return id, storageErr(err)
}

func (s *Store) PartURL(ctx context.Context, key, id string, number int, ttl time.Duration) (string, error) {
	u, err := s.client.Presign(ctx, http.MethodPut, s.bucket, key, ttl, url.Values{"uploadId": {id}, "partNumber": {strconv.Itoa(number)}})
	if err != nil {
		return "", storageErr(err)
	}
	return u.String(), nil
}

func (s *Store) ListParts(ctx context.Context, key, id string) ([]ports.Part, error) {
	var out []ports.Part
	marker := 0
	for {
		page, err := s.client.ListObjectParts(ctx, s.bucket, key, id, marker, 1000)
		if err != nil {
			return nil, storageErr(err)
		}
		for _, p := range page.ObjectParts {
			out = append(out, ports.Part{Number: p.PartNumber, Size: p.Size, ETag: strings.Trim(p.ETag, "\"")})
		}
		if !page.IsTruncated {
			return out, nil
		}
		if page.NextPartNumberMarker <= marker {
			return nil, errors.New("s3: part listing did not advance")
		}
		marker = page.NextPartNumberMarker
	}
}

func (s *Store) FinishMultipart(ctx context.Context, key, id string, parts []ports.Part) error {
	items := make([]minio.CompletePart, len(parts))
	for i, p := range parts {
		items[i] = minio.CompletePart{PartNumber: p.Number, ETag: p.ETag}
	}
	_, err := s.client.CompleteMultipartUpload(ctx, s.bucket, key, id, items, minio.PutObjectOptions{})
	return storageErr(err)
}

func (s *Store) AbortMultipart(ctx context.Context, key, id string) error {
	var err error
	if id == "" {
		if s.native.Load() < 0 {
			return nil
		}
		err = s.client.RemoveIncompleteUpload(ctx, s.bucket, key)
	} else {
		err = s.client.AbortMultipartUpload(ctx, s.bucket, key, id)
	}
	if minio.ToErrorResponse(err).Code == "NoSuchUpload" {
		return nil
	}
	return storageErr(err)
}
