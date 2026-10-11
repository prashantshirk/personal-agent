package storage

import (
	"context"
	"fmt"

	"secondbrain/internal/config"
)

// Store keeps application objects encrypted at rest.
type Store interface {
	UploadEncrypted(ctx context.Context, key, source, contentType string) error
	DownloadDecrypted(ctx context.Context, key, dest string) error
	Delete(ctx context.Context, key string) error
}

func New(ctx context.Context, cfg config.Config) (Store, error) {
	switch cfg.StorageBackend {
	case "none":
		return nil, nil
	case "local":
		return NewLocal(cfg)
	case "r2":
		return NewR2(ctx, cfg)
	default:
		return nil, fmt.Errorf("unsupported file storage backend %q", cfg.StorageBackend)
	}
}
