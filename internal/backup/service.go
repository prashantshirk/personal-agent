package backup

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"secondbrain/internal/db"
	"secondbrain/internal/storage"
)

type Service struct {
	db               *db.DB
	store            storage.Store
	dataDir, tempDir string
}

func New(database *db.DB, store storage.Store, dataDir, tempDir string) *Service {
	return &Service{db: database, store: store, dataDir: dataDir, tempDir: tempDir}
}

// Restore decrypts one backup, checks SQLite integrity, and refuses to overwrite an existing file.
func Restore(ctx context.Context, store storage.Store, key, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("restore destination already exists")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".secondbrain-restore-*.db")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(tmpPath)
	defer os.Remove(tmpPath)
	if err = store.DownloadDecrypted(ctx, key, tmpPath); err != nil {
		return err
	}
	check, err := sql.Open("sqlite", tmpPath)
	if err != nil {
		return err
	}
	defer check.Close()
	var result string
	if err = check.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("restored database integrity check failed: %s", result)
	}
	if err = check.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, dest)
}

// Run uses SQLite VACUUM INTO so committed WAL data is included in a consistent snapshot.
func (s *Service) Run(ctx context.Context) (string, error) {
	if s.store == nil {
		return "", fmt.Errorf("backup storage is not configured")
	}
	if err := os.MkdirAll(s.tempDir, 0700); err != nil {
		return "", err
	}
	snapshot, err := os.CreateTemp(s.tempDir, "secondbrain-backup-*.db")
	if err != nil {
		return "", err
	}
	path := snapshot.Name()
	_ = snapshot.Close()
	_ = os.Remove(path)
	defer os.Remove(path)
	if _, err = s.db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return "", fmt.Errorf("create SQLite snapshot: %w", err)
	}
	stat, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	key, err := storage.RandomKey()
	if err != nil {
		return "", err
	}
	key = "backups/" + filepath.Base(key) + "-" + time.Now().UTC().Format("20060102T150405Z") + ".db.age"
	if err = s.store.UploadEncrypted(ctx, key, path, "application/octet-stream"); err != nil {
		return "", err
	}
	created := time.Now().UTC()
	if _, err = s.db.ExecContext(ctx, `INSERT INTO backups(object_key,created_at,size) VALUES(?,?,?)`, key, created.Format(time.RFC3339Nano), stat.Size()); err != nil {
		return key, fmt.Errorf("backup uploaded but database record failed: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT object_key FROM backups WHERE created_at<?`, created.Add(-14*24*time.Hour).Format(time.RFC3339Nano))
	if err != nil {
		return key, err
	}
	var expired []string
	for rows.Next() {
		var old string
		if err = rows.Scan(&old); err != nil {
			rows.Close()
			return key, err
		}
		expired = append(expired, old)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return key, err
	}
	rows.Close()
	for _, old := range expired {
		if err = s.store.Delete(ctx, old); err != nil {
			return key, fmt.Errorf("backup saved, but retention delete failed: %w", err)
		}
		if _, err = s.db.ExecContext(ctx, `DELETE FROM backups WHERE object_key=?`, old); err != nil {
			return key, err
		}
	}
	return key, nil
}
