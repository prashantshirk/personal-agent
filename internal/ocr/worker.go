package ocr

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"secondbrain/internal/config"
	"secondbrain/internal/db"
	"secondbrain/internal/search"
	"secondbrain/internal/storage"
	"secondbrain/internal/telegram"
)

const jobInterval = time.Second
const commandTimeout = 2 * time.Minute

var pageCountPattern = regexp.MustCompile(`(?m)^Pages:\s+(\d+)\s*$`)

type Worker struct {
	cfg     config.Config
	db      *db.DB
	storage storage.Store
	api     *telegram.Client
	chatID  int64
	log     *slog.Logger
}
type fileJob struct {
	FileID int64 `json:"file_id"`
}

func NewWorker(cfg config.Config, database *db.DB, store storage.Store, api *telegram.Client, chatID int64, logger *slog.Logger) *Worker {
	return &Worker{cfg: cfg, db: database, storage: store, api: api, chatID: chatID, log: logger}
}

func (w *Worker) Run(ctx context.Context) {
	if _, err := w.db.ExecContext(ctx, `UPDATE jobs SET status='pending',started_at=NULL WHERE status='running'`); err != nil {
		w.log.Error("reset interrupted jobs failed", "error", err)
	}
	ticker := time.NewTicker(jobInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		w.runOne(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) runOne(ctx context.Context) {
	var id int64
	var typ, payload string
	err := w.db.QueryRowContext(ctx, `SELECT id,type,payload FROM jobs WHERE status='pending' ORDER BY id LIMIT 1`).Scan(&id, &typ, &payload)
	if err == sql.ErrNoRows {
		return
	}
	if err != nil {
		w.log.Error("read next job failed", "error", err)
		return
	}
	_, err = w.db.ExecContext(ctx, `UPDATE jobs SET status='running',attempts=attempts+1,started_at=? WHERE id=? AND status='pending'`, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		w.log.Error("claim job failed", "job_id", id, "error", err)
		return
	}
	if typ != "index_file" {
		w.finishJob(ctx, id, fmt.Errorf("unsupported job type %q", typ))
		return
	}
	var job fileJob
	if err = json.Unmarshal([]byte(payload), &job); err != nil {
		w.finishJob(ctx, id, err)
		return
	}
	if err = w.indexFile(ctx, job.FileID); err != nil {
		w.finishJob(ctx, id, err)
		return
	}
	_, err = w.db.ExecContext(ctx, `UPDATE jobs SET status='done',finished_at=?,error=NULL WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		w.log.Error("finish job failed", "job_id", id, "error", err)
	}
}

func (w *Worker) finishJob(ctx context.Context, id int64, err error) {
	_, _ = w.db.ExecContext(ctx, `UPDATE jobs SET status='failed',error=?,finished_at=? WHERE id=?`, err.Error(), time.Now().UTC().Format(time.RFC3339Nano), id)
	w.log.Error("background job failed", "job_id", id, "error", err)
}

func (w *Worker) indexFile(ctx context.Context, id int64) error {
	var name, mimeType, key, caption, tags string
	var progressID int64
	if err := w.db.QueryRowContext(ctx, `SELECT name,COALESCE(mime,''),r2_key,COALESCE(caption,''),COALESCE(tags,''),COALESCE(progress_message_id,0) FROM files WHERE id=?`, id).Scan(&name, &mimeType, &key, &caption, &tags, &progressID); err != nil {
		return err
	}
	if strings.HasPrefix(mimeType, "video/") {
		_, err := w.db.ExecContext(ctx, `UPDATE files SET index_status='skipped',pages=0,ocr_pages=0 WHERE id=?`, id)
		if err == nil {
			w.progress(progressID, fmt.Sprintf("Saved %s. Video indexing is skipped; it can be found by name, caption, tags and date.", name))
		}
		return err
	}
	if err := os.MkdirAll(w.cfg.TempDir, 0700); err != nil {
		return err
	}
	local, err := os.CreateTemp(w.cfg.TempDir, "index-*")
	if err != nil {
		return err
	}
	localPath := local.Name()
	_ = local.Close()
	_ = os.Remove(localPath)
	defer os.Remove(localPath)
	if err = w.storage.DownloadDecrypted(ctx, key, localPath); err != nil {
		return err
	}
	_, _ = w.db.ExecContext(ctx, `UPDATE files SET index_status='running' WHERE id=?`, id)
	switch {
	case mimeType == "application/pdf" || strings.EqualFold(filepath.Ext(name), ".pdf"):
		err = w.indexPDF(ctx, id, name, caption, tags, localPath, progressID)
	case strings.HasPrefix(mimeType, "image/"):
		err = w.indexImage(ctx, id, name, caption, tags, localPath, progressID)
	case mimeType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document" || strings.EqualFold(filepath.Ext(name), ".docx"):
		err = w.indexDocx(ctx, id, name, caption, tags, localPath, progressID)
	default:
		_, err = w.db.ExecContext(ctx, `UPDATE files SET index_status='skipped',pages=0,ocr_pages=0 WHERE id=?`, id)
		if err == nil {
			w.progress(progressID, fmt.Sprintf("Saved %s. This file type is stored but not OCR-indexed.", name))
		}
		return err
	}
	if err != nil {
		_, _ = w.db.ExecContext(ctx, `UPDATE files SET index_status='failed' WHERE id=?`, id)
		w.progress(progressID, fmt.Sprintf("Indexing failed for %s.", name))
		return err
	}
	return nil
}

func (w *Worker) indexPDF(ctx context.Context, id int64, name, caption, tags, path string, progressID int64) error {
	info, err := runCommand(ctx, commandTimeout, "pdfinfo", path)
	if err != nil {
		return fmt.Errorf("pdfinfo failed: %w", err)
	}
	matches := pageCountPattern.FindSubmatch(info)
	if len(matches) != 2 {
		return fmt.Errorf("pdfinfo did not report page count")
	}
	pages, _ := strconv.Atoi(string(matches[1]))
	if pages < 1 || pages > 100000 {
		return fmt.Errorf("PDF page count %d is outside supported bounds", pages)
	}
	imagePages := map[int]bool{}
	if w.cfg.OCRImagePages {
		imagePages, err = w.findImagePages(ctx, path)
		if err != nil {
			w.log.Warn("pdfimages page discovery failed", "file_id", id, "error", err)
		}
	}
	ocrCount := 0
	for page := 1; page <= pages; page++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var exists int
		if e := w.db.QueryRowContext(ctx, `SELECT count(*) FROM file_pages WHERE file_id=? AND page=?`, id, page).Scan(&exists); e != nil {
			return e
		}
		if exists > 0 {
			continue
		}
		pageCtx, cancel := context.WithTimeout(ctx, commandTimeout)
		text, e := runCommand(pageCtx, commandTimeout, "pdftotext", "-f", strconv.Itoa(page), "-l", strconv.Itoa(page), "-layout", path, "-")
		cancel()
		if e != nil {
			return fmt.Errorf("extract page %d text: %w", page, e)
		}
		body := string(text)
		source := "text"
		if utf8.RuneCountInString(strings.TrimSpace(body)) < w.cfg.OCRMinTextChars {
			ocr, e := w.ocrPDFPage(ctx, path, page, id)
			if e != nil {
				return fmt.Errorf("OCR page %d: %w", page, e)
			}
			body = strings.TrimSpace(body + "\n" + ocr)
			source = "ocr"
			ocrCount++
		}
		if imagePages[page] {
			embedded, e := w.ocrEmbeddedImages(ctx, path, page, id)
			if e != nil {
				w.log.Warn("embedded image OCR failed", "file_id", id, "page", page, "error", e)
			} else if strings.TrimSpace(embedded) != "" {
				body = strings.TrimSpace(body + "\n" + embedded)
				if source == "text" {
					source = "ocr+text"
				}
				ocrCount++
			}
		}
		if e = w.storePage(ctx, id, page, name, tags, source, body); e != nil {
			return e
		}
		if page%5 == 0 || page == pages {
			w.progress(progressID, fmt.Sprintf("Indexing %s %s — %d/%d pages", name, progressBar(page, pages), page, pages))
		}
	}
	var ocrPages int
	if err := w.db.QueryRowContext(ctx, `SELECT count(*) FROM file_pages WHERE file_id=? AND source IN ('ocr','ocr+text')`, id).Scan(&ocrPages); err != nil {
		return err
	}
	_, err = w.db.ExecContext(ctx, `UPDATE files SET index_status='done',pages=?,ocr_pages=? WHERE id=?`, pages, ocrPages, id)
	if err == nil {
		w.progress(progressID, fmt.Sprintf("✅ Indexed %s: %d pages, %d OCR'd.", name, pages, ocrPages))
	}
	_ = ocrCount
	return err
}

// progressBar renders a small text progress bar for edited progress
// messages, e.g. "▓▓▓▓░░░░ 40%". Telegram has no native progress bar.
func progressBar(done, total int) string {
	if total <= 0 {
		return ""
	}
	pct := done * 100 / total
	filled := pct * 10 / 100
	return fmt.Sprintf("%s%s %d%%", strings.Repeat("▓", filled), strings.Repeat("░", 10-filled), pct)
}

func (w *Worker) indexImage(ctx context.Context, id int64, name, caption, tags, path string, progressID int64) error {
	text, err := w.bestOCR(ctx, path)
	if err != nil {
		return err
	}
	body := strings.TrimSpace(caption + "\n" + text)
	if err = w.storePage(ctx, id, 1, name, tags, "ocr", body); err != nil {
		return err
	}
	_, err = w.db.ExecContext(ctx, `UPDATE files SET index_status='done',pages=1,ocr_pages=1 WHERE id=?`, id)
	if err == nil {
		if strings.TrimSpace(text) == "" {
			w.progress(progressID, fmt.Sprintf("Indexed %s: OCR found no readable text. For sharper text, send the image as a File (not a Photo) — Telegram compresses photos.", name))
		} else {
			w.progress(progressID, fmt.Sprintf("✅ Indexed %s: 1 image OCR'd.", name))
		}
	}
	return err
}

func (w *Worker) indexDocx(ctx context.Context, id int64, name, caption, tags, path string, progressID int64) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("open docx: %w", err)
	}
	defer zr.Close()
	var textB strings.Builder
	var media []*zip.File
	for _, f := range zr.File {
		switch {
		case f.Name == "word/document.xml" || strings.HasPrefix(f.Name, "word/header") || strings.HasPrefix(f.Name, "word/footer"):
			rc, e := f.Open()
			if e != nil {
				return e
			}
			e = decodeDocxPart(rc, &textB)
			_ = rc.Close()
			if e != nil {
				return e
			}
		case strings.HasPrefix(f.Name, "word/media/"):
			switch strings.ToLower(filepath.Ext(f.Name)) {
			case ".png", ".jpg", ".jpeg":
				media = append(media, f)
			}
		}
	}
	page, ocrPages := 0, 0
	if wordText := strings.TrimSpace(textB.String()); wordText != "" || strings.TrimSpace(caption) != "" {
		page++
		if e := w.storePage(ctx, id, page, name, tags, "text", strings.TrimSpace(caption+"\n"+wordText)); e != nil {
			return e
		}
	}
	for i, f := range media {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		txt, e := w.ocrZipImage(ctx, f, id, i)
		if e != nil {
			w.log.Warn("docx image OCR failed", "file_id", id, "entry", f.Name, "error", e)
			continue
		}
		if strings.TrimSpace(txt) == "" {
			continue
		}
		page++
		ocrPages++
		if e := w.storePage(ctx, id, page, name, tags, "ocr", strings.TrimSpace(txt)); e != nil {
			return e
		}
		if page%3 == 0 {
			w.progress(progressID, fmt.Sprintf("Indexing %s: %d sections", name, page))
		}
	}
	if page == 0 {
		page = 1
		if e := w.storePage(ctx, id, 1, name, tags, "text", strings.TrimSpace(caption)); e != nil {
			return e
		}
	}
	_, err = w.db.ExecContext(ctx, `UPDATE files SET index_status='done',pages=?,ocr_pages=? WHERE id=?`, page, ocrPages, id)
	if err == nil {
		w.progress(progressID, fmt.Sprintf("Indexed %s: %d sections, %d OCR'd.", name, page, ocrPages))
	}
	return err
}

// ocrZipImage extracts one embedded image from the docx archive and OCRs it, one at a time to keep memory bounded.
func (w *Worker) ocrZipImage(ctx context.Context, f *zip.File, id int64, idx int) (string, error) {
	rc, err := f.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	tmp := filepath.Join(w.cfg.TempDir, fmt.Sprintf("sb-docx-%d-%d%s", id, idx, strings.ToLower(filepath.Ext(f.Name))))
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(out, io.LimitReader(rc, 50<<20))
	closeErr := out.Close()
	defer os.Remove(tmp)
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return w.runTesseract(ctx, tmp)
}

func decodeDocxPart(r io.Reader, b *strings.Builder) error {
	dec := xml.NewDecoder(r)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				var s string
				if err := dec.DecodeElement(&s, &t); err != nil {
					return err
				}
				b.WriteString(s)
			case "tab":
				b.WriteByte('\t')
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "p", "br", "cr":
				b.WriteByte('\n')
			}
		}
	}
}

func (w *Worker) storePage(ctx context.Context, id int64, page int, title, tags, source, body string) error {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO file_pages(file_id,page,source,body) VALUES(?,?,?,?) ON CONFLICT(file_id,page) DO UPDATE SET source=excluded.source,body=excluded.body`, id, page, source, body); err != nil {
		return err
	}
	if err = search.IndexFilePage(ctx, tx, id, page, title, tags, body); err != nil {
		return err
	}
	return tx.Commit()
}

func (w *Worker) ocrPDFPage(ctx context.Context, pdf string, page int, id int64) (string, error) {
	prefix := filepath.Join(w.cfg.TempDir, fmt.Sprintf("sb-%d-%d", id, page))
	image := prefix + ".png"
	defer os.Remove(image)
	if _, err := runCommand(ctx, commandTimeout, "pdftoppm", "-f", strconv.Itoa(page), "-l", strconv.Itoa(page), "-singlefile", "-r", strconv.Itoa(w.cfg.OCRDPI), "-png", "-gray", pdf, prefix); err != nil {
		return "", err
	}
	return w.bestOCR(ctx, image)
}

func (w *Worker) runTesseract(ctx context.Context, path string) (string, error) {
	return w.runTesseractPSM(ctx, path, "6")
}

// runTesseractPSM runs tesseract with an explicit page segmentation mode.
// PSM 6 (uniform block of text) is the best default for documents and
// screenshots, where the old default PSM 3 often mis-split the page.
func (w *Worker) runTesseractPSM(ctx context.Context, path, psm string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "tesseract", path, "stdout", "--oem", "1", "--psm", psm, "-l", w.cfg.OCRLangs, "-c", "preserve_interword_spaces=1")
	cmd.Env = append(os.Environ(), "OMP_THREAD_LIMIT=1")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("tesseract: %w", err)
	}
	return string(out), nil
}

// bestOCR tries several cheap, single-image passes and keeps the longest
// readable result. This matters because Telegram photos are compressed
// and low-contrast, and one PSM rarely fits every photo. It stays
// sequential (one image at a time) to respect the small VM, and any
// preprocessing failure falls back to the raw pass instead of failing.
func (w *Worker) bestOCR(ctx context.Context, path string) (string, error) {
	best, err := w.runTesseractPSM(ctx, path, "6")
	if err != nil {
		return "", err
	}
	if utf8.RuneCountInString(strings.TrimSpace(best)) >= 20 {
		return best, nil
	}
	if pre, cleanup, err := w.preprocessImage(ctx, path); err == nil {
		defer cleanup()
		if text, err := w.runTesseractPSM(ctx, pre, "6"); err == nil && utf8.RuneCountInString(strings.TrimSpace(text)) > utf8.RuneCountInString(strings.TrimSpace(best)) {
			best = text
		}
		if utf8.RuneCountInString(strings.TrimSpace(best)) < 20 {
			if text, err := w.runTesseractPSM(ctx, pre, "11"); err == nil && utf8.RuneCountInString(strings.TrimSpace(text)) > utf8.RuneCountInString(strings.TrimSpace(best)) {
				best = text
			}
		}
	}
	return best, nil
}

// preprocessImage returns a grayscale, contrast-normalised, sharpened copy
// of an image, upscaled to at least 2000 px wide if it is smaller.
// Tesseract is very sensitive to small, low-contrast text, which is
// exactly what compressed Telegram photos produce. Uses ImageMagick
// (convert, falling back to magick); returns an error if neither exists.
func (w *Worker) preprocessImage(ctx context.Context, path string) (string, func(), error) {
	cleanup := func() {}
	out, err := os.CreateTemp(w.cfg.TempDir, "ocr-pre-*.png")
	if err != nil {
		return "", cleanup, err
	}
	outPath := out.Name()
	_ = out.Close()
	cleanup = func() { _ = os.Remove(outPath) }
	width := 0
	if info, err := runCommand(ctx, commandTimeout, "identify", "-format", "%w", path); err == nil {
		width, _ = strconv.Atoi(strings.TrimSpace(string(info)))
	}
	args := []string{path, "-colorspace", "Gray", "-normalize", "-sharpen", "0x1"}
	if width > 0 && width < 2000 {
		args = append(args, "-resize", "2000x")
	}
	args = append(args, outPath)
	for _, tool := range []string{"convert", "magick"} {
		if _, err := runCommand(ctx, commandTimeout, tool, args...); err == nil {
			return outPath, cleanup, nil
		}
	}
	cleanup()
	return "", func() {}, fmt.Errorf("no ImageMagick convert/magick available")
}

func (w *Worker) findImagePages(ctx context.Context, pdf string) (map[int]bool, error) {
	out, err := runCommand(ctx, commandTimeout, "pdfimages", "-list", pdf)
	if err != nil {
		return nil, err
	}
	found := map[int]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		page, e1 := strconv.Atoi(fields[0])
		width, e2 := strconv.Atoi(fields[3])
		height, e3 := strconv.Atoi(fields[4])
		if e1 == nil && e2 == nil && e3 == nil && width >= 300 && height >= 100 && int64(width)*int64(height) >= 90000 {
			found[page] = true
		}
	}
	return found, nil
}

func (w *Worker) ocrEmbeddedImages(ctx context.Context, pdf string, page int, id int64) (string, error) {
	prefix := filepath.Join(w.cfg.TempDir, fmt.Sprintf("sb-%d-%d-img", id, page))
	if _, err := runCommand(ctx, commandTimeout, "pdfimages", "-f", strconv.Itoa(page), "-l", strconv.Itoa(page), "-png", pdf, prefix); err != nil {
		return "", err
	}
	images, err := filepath.Glob(prefix + "*")
	if err != nil {
		return "", err
	}
	defer func() {
		for _, image := range images {
			_ = os.Remove(image)
		}
	}()
	var texts []string
	for _, image := range images {
		if txt, e := w.runTesseract(ctx, image); e == nil && strings.TrimSpace(txt) != "" {
			texts = append(texts, txt)
		}
	}
	return strings.Join(texts, "\n"), nil
}

func runCommand(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s: %w (%s)", name, err, strings.TrimSpace(string(out)))
	}
	return out, nil
}
func (w *Worker) progress(messageID int64, text string) {
	if messageID == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.api.EditMessageText(ctx, w.chatID, messageID, text); err != nil {
		w.log.Debug("progress message edit failed", "error", err)
	}
}
