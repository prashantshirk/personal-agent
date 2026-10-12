package files

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"secondbrain/internal/db"
	"secondbrain/internal/search"
	"secondbrain/internal/storage"
	"secondbrain/internal/telegram"
)

type Service struct {
	db      *db.DB
	storage storage.Store
}
type Saved struct {
	ID        int64
	Name      string
	Size      int64
	Duplicate bool
}

func New(database *db.DB, store storage.Store) *Service {
	return &Service{db: database, storage: store}
}

func (s *Service) Save(ctx context.Context, path, name, mimeType, caption string) (Saved, error) {
	if s.storage == nil {
		return Saved{}, fmt.Errorf("file storage is not configured")
	}
	name = safeName(name)
	file, err := os.Open(path)
	if err != nil {
		return Saved{}, err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return Saved{}, copyErr
	}
	if closeErr != nil {
		return Saved{}, closeErr
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	var existingID int64
	var existingName string
	err = s.db.QueryRowContext(ctx, `SELECT id,name FROM files WHERE sha256=?`, digest).Scan(&existingID, &existingName)
	if err == nil {
		return Saved{ID: existingID, Name: existingName, Size: size, Duplicate: true}, nil
	}
	if err != sql.ErrNoRows {
		return Saved{}, err
	}
	key, err := storage.RandomKey()
	if err != nil {
		return Saved{}, err
	}
	if err = s.storage.UploadEncrypted(ctx, key, path, mimeType); err != nil {
		return Saved{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		_ = s.storage.Delete(ctx, key)
		return Saved{}, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO files(created_at,name,mime,size,sha256,r2_key,encrypted,caption,tags,index_status) VALUES(?,?,?,?,?,?,1,?,?,'pending')`, time.Now().UTC().Format(time.RFC3339Nano), name, mimeType, size, digest, key, caption, caption)
	if err != nil {
		_ = s.storage.Delete(ctx, key)
		return Saved{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		_ = s.storage.Delete(ctx, key)
		return Saved{}, err
	}
	if err = search.IndexFileMetadata(ctx, tx, id, name, caption, caption); err != nil {
		_ = s.storage.Delete(ctx, key)
		return Saved{}, err
	}
	payload, _ := json.Marshal(map[string]int64{"file_id": id})
	if _, err = tx.ExecContext(ctx, `INSERT INTO jobs(type,payload,status,created_at) VALUES('index_file',?,'pending',?)`, string(payload), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		_ = s.storage.Delete(ctx, key)
		return Saved{}, err
	}
	if err = tx.Commit(); err != nil {
		_ = s.storage.Delete(ctx, key)
		return Saved{}, err
	}
	return Saved{ID: id, Name: name, Size: size}, nil
}
func (s *Service) SetProgressMessage(ctx context.Context, id, messageID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE files SET progress_message_id=? WHERE id=?`, messageID, id)
	return err
}
func (s *Service) Search(ctx context.Context, query string, limit, offset int) ([]search.Result, string, error) {
	query += " type:file"
	return search.Query(ctx, s.db.DB, query, limit, offset)
}
func (s *Service) Recent(ctx context.Context) (string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,created_at,size,index_status FROM files ORDER BY created_at DESC LIMIT 20`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id, size int64
		var name, created, status string
		if err := rows.Scan(&id, &name, &created, &size, &status); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "#%d %s (%s, %s)\n", id, name, formatBytes(size), status)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if b.Len() == 0 {
		return "No files saved yet.", nil
	}
	return b.String(), nil
}
type FileRow struct {
	ID, Size int64
	Name, Status, Mime string
}

// Category buckets a file for the /files browser.
func Category(mime, name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case strings.HasPrefix(mime, "image/"):
		return "photo"
	case strings.HasPrefix(mime, "video/"):
		return "video"
	case mime == "application/pdf" || ext == ".pdf" || ext == ".docx" || ext == ".doc" || ext == ".txt" || ext == ".md" || strings.Contains(mime, "word") || strings.Contains(mime, "document"):
		return "document"
	case ext == ".go" || ext == ".py" || ext == ".js" || ext == ".ts" || ext == ".java" || ext == ".c" || ext == ".cpp" || ext == ".rs" || ext == ".sh" || ext == ".json" || ext == ".html" || ext == ".css" || ext == ".yml" || ext == ".yaml":
		return "code"
	default:
		return "other"
	}
}

// List returns recent files in a category (photo, document, video, code, other).
func (s *Service) List(ctx context.Context, category string, limit int) ([]FileRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,COALESCE(mime,''),size,index_status FROM files ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileRow
	for rows.Next() {
		var r FileRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Mime, &r.Size, &r.Status); err != nil {
			return nil, err
		}
		if Category(r.Mime, r.Name) != category {
			continue
		}
		out = append(out, r)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, rows.Err()
}

func (s *Service) Tag(ctx context.Context, id int64, tags string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var name, caption string
	if err = tx.QueryRowContext(ctx, `SELECT name,caption FROM files WHERE id=?`, id).Scan(&name, &caption); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE files SET tags=? WHERE id=?`, tags, id); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT page,source,body FROM file_pages WHERE file_id=? ORDER BY page`, id)
	if err != nil {
		return err
	}
	type page struct {
		n            int
		source, body string
	}
	var pages []page
	for rows.Next() {
		var p page
		if err = rows.Scan(&p.n, &p.source, &p.body); err != nil {
			rows.Close()
			return err
		}
		pages = append(pages, p)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, table := range []string{"search_fts", "search_tri"} {
		if _, err = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE kind='file' AND CAST(ref_id AS INTEGER)=?`, id); err != nil {
			return err
		}
	}
	if err = search.IndexFileMetadata(ctx, tx, id, name, caption, tags); err != nil {
		return err
	}
	for _, p := range pages {
		if err = search.IndexFilePage(ctx, tx, id, p.n, name, tags, p.body); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Service) SendToChat(ctx context.Context, id, chatID int64, tempDir string, api *telegram.Client) error {
	var key, name, mimeType string
	if err := s.db.QueryRowContext(ctx, `SELECT r2_key,name,COALESCE(mime,'application/octet-stream') FROM files WHERE id=?`, id).Scan(&key, &name, &mimeType); err != nil {
		return err
	}
	if err := os.MkdirAll(tempDir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(tempDir, "retrieve-*")
	if err != nil {
		return err
	}
	path := file.Name()
	_ = file.Close()
	_ = os.Remove(path)
	defer os.Remove(path)
	if err = s.storage.DownloadDecrypted(ctx, key, path); err != nil {
		return err
	}
	return api.SendFile(ctx, chatID, path, name, mimeType)
}
// PreviewImage produces a small preview image for a file (first PDF page, the photo itself,
// or the first embedded docx image). Returns the image path and a cleanup func the caller must run.
func (s *Service) PreviewImage(ctx context.Context, id int64, tempDir string) (string, func(), error) {
	var key, name, mimeType string
	if err := s.db.QueryRowContext(ctx, `SELECT r2_key,name,COALESCE(mime,'') FROM files WHERE id=?`, id).Scan(&key, &name, &mimeType); err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(tempDir, 0700); err != nil {
		return "", nil, err
	}
	raw, err := os.CreateTemp(tempDir, "prev-raw-*")
	if err != nil {
		return "", nil, err
	}
	rawPath := raw.Name()
	_ = raw.Close()
	_ = os.Remove(rawPath)
	if err := s.storage.DownloadDecrypted(ctx, key, rawPath); err != nil {
		return "", nil, err
	}
	cleanupRaw := func() { _ = os.Remove(rawPath) }
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case strings.HasPrefix(mimeType, "image/") || Category(mimeType, name) == "photo":
		return rawPath, cleanupRaw, nil
	case mimeType == "application/pdf" || ext == ".pdf":
		defer cleanupRaw()
		prefix := filepath.Join(tempDir, fmt.Sprintf("prev-%d", id))
		out := prefix + ".png"
		cmd := exec.CommandContext(ctx, "pdftoppm", "-f", "1", "-l", "1", "-singlefile", "-r", "150", "-png", "-gray", rawPath, prefix)
		if combined, err := cmd.CombinedOutput(); err != nil {
			return "", nil, fmt.Errorf("pdftoppm: %w (%s)", err, strings.TrimSpace(string(combined)))
		}
		return out, func() { _ = os.Remove(out) }, nil
	case ext == ".docx" || strings.Contains(mimeType, "word") || strings.Contains(mimeType, "document"):
		defer cleanupRaw()
		img, err := extractFirstDocxImage(rawPath, tempDir, id)
		if err != nil {
			return "", nil, err
		}
		return img, func() { _ = os.Remove(img) }, nil
	}
	cleanupRaw()
	return "", nil, fmt.Errorf("no preview available for this file type")
}

func extractFirstDocxImage(path, tempDir string, id int64) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, "word/media/") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(f.Name))
		if ext != ".png" && ext != ".jpg" && ext != ".jpeg" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		out := filepath.Join(tempDir, fmt.Sprintf("prev-docx-%d%s", id, ext))
		w, err := os.Create(out)
		if err != nil {
			_ = rc.Close()
			return "", err
		}
		_, copyErr := io.Copy(w, io.LimitReader(rc, 50<<20))
		_ = w.Close()
		_ = rc.Close()
		if copyErr != nil {
			_ = os.Remove(out)
			return "", copyErr
		}
		return out, nil
	}
	return "", fmt.Errorf("document has no embedded image to preview")
}

func (s *Service) RequestDelete(ctx context.Context, id int64) (int64, error) {
	var name string
	if err := s.db.QueryRowContext(ctx, `SELECT name FROM files WHERE id=?`, id).Scan(&name); err != nil {
		return 0, err
	}
	payload, _ := json.Marshal(map[string]int64{"file_id": id})
	res, err := s.db.ExecContext(ctx, `INSERT INTO pending_actions(kind,payload,created_at,expires_at) VALUES('delete_file',?,?,?)`, string(payload), time.Now().UTC().Format(time.RFC3339Nano), time.Now().Add(10*time.Minute).UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}
func (s *Service) FinishDelete(ctx context.Context, actionID int64, confirm bool) error {
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM pending_actions WHERE id=? AND kind='delete_file' AND status='pending' AND expires_at>?`, actionID, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&payload)
	if err != nil {
		return err
	}
	var value struct {
		FileID int64 `json:"file_id"`
	}
	if err = json.Unmarshal([]byte(payload), &value); err != nil {
		return err
	}
	status := "rejected"
	if confirm {
		var key string
		if err = s.db.QueryRowContext(ctx, `SELECT r2_key FROM files WHERE id=?`, value.FileID).Scan(&key); err != nil {
			return err
		}
		if err = s.storage.Delete(ctx, key); err != nil {
			return err
		}
		tx, txErr := s.db.BeginTx(ctx, nil)
		if txErr != nil {
			return txErr
		}
		defer tx.Rollback()
		for _, table := range []string{"search_fts", "search_tri"} {
			if _, txErr = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE kind='file' AND CAST(ref_id AS INTEGER)=?`, value.FileID); txErr != nil {
				return txErr
			}
		}
		if _, txErr = tx.ExecContext(ctx, `DELETE FROM files WHERE id=?`, value.FileID); txErr != nil {
			return txErr
		}
		if _, txErr = tx.ExecContext(ctx, `DELETE FROM jobs WHERE type='index_file' AND payload=?`, payload); txErr != nil {
			return txErr
		}
		status = "confirmed"
		if txErr = tx.Commit(); txErr != nil {
			return txErr
		}
	}
	_, err = s.db.ExecContext(ctx, `UPDATE pending_actions SET status=? WHERE id=?`, status, actionID)
	return err
}
func safeName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "file"
	}
	var b strings.Builder
	for _, r := range name {
		if r < 32 || r == 127 || r == '/' || r == '\\' {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "file"
	}
	return out
}
func formatBytes(size int64) string {
	if size >= 1<<20 {
		return fmt.Sprintf("%.1f MB", float64(size)/(1<<20))
	}
	if size >= 1<<10 {
		return fmt.Sprintf("%.1f KB", float64(size)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", size)
}
