package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strings"

	"github.com/LUOSENGWA/MFGraph/internal/blob/posix"
	"github.com/LUOSENGWA/MFGraph/internal/blob/s3"
	"github.com/LUOSENGWA/MFGraph/internal/config"
	"github.com/LUOSENGWA/MFGraph/internal/core"
	"github.com/LUOSENGWA/MFGraph/internal/ports"
)

type checkedStore interface {
	ports.BlobStore
	Check(context.Context) error
}

func openStorage(cfg config.Config, log *slog.Logger) (checkedStore, string, error) {
	if cfg.BlobBackend == "s3" {
		store, err := s3.New(s3.Config{Endpoint: cfg.S3Endpoint, Bucket: cfg.S3Bucket, Region: cfg.S3Region,
			AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey, SessionToken: cfg.S3SessionToken}, log)
		// Keep credentials out of backend identity so rotating them is safe.
		origin := strings.TrimSuffix(cfg.S3Endpoint, "/")
		id := sha256.Sum256([]byte(origin + "/" + cfg.S3Bucket))
		return store, fmt.Sprintf("s3-%x", id[:16]), err
	}
	store, err := posix.New(cfg.StorageRoot, log)
	return store, core.DefaultStorageBackendID, err
}
