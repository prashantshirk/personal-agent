package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"secondbrain/internal/db"
	"secondbrain/internal/files"
	"secondbrain/internal/llm"
	"secondbrain/internal/reminders"
	"secondbrain/internal/telegram"
)

// Service runs proactive scheduled tasks: reports, messages, and timed file sends.
type Service struct {
	db      *db.DB
	api     *telegram.Client
	brain   *llm.Brain
	files   *files.Service
	log     *slog.Logger
	loc     *time.Location
	tempDir string
}

func New(database *db.DB, api *telegram.Client, brain *llm.Brain, fileService *files.Service, loc *time.Location, tempDir string, logger *slog.Logger) *Service {
	return &Service{db: database, api: api, brain: brain, files: fileService, loc: loc, tempDir: tempDir, log: logger}
}

func (s *Service) Run(ctx context.Context) {
	if _, err := s.db.ExecContext(ctx, `UPDATE schedules SET status='scheduled' WHERE status='running'`); err != nil {
		s.log.Error("recover interrupted schedules failed", "error", err)
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	s.fireDue(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.fireDue(ctx)
		}
	}
}

func (s *Service) fireDue(ctx context.Context) {
	now := time.Now().UTC()
	rows, err := s.db.QueryContext(ctx, `SELECT id,chat_id,kind,prompt,COALESCE(file_id,0),next_at,COALESCE(repeat_rule,'') FROM schedules WHERE status='scheduled' AND next_at<=? ORDER BY next_at LIMIT 10`, now.Format(time.RFC3339Nano))
	if err != nil {
		s.log.Error("select schedules failed", "error", err)
		return
	}
	type sch struct {
		id, chatID, fileID         int64
		kind, prompt, nextAt, rule string
	}
	var due []sch
	for rows.Next() {
		var x sch
		if err := rows.Scan(&x.id, &x.chatID, &x.kind, &x.prompt, &x.fileID, &x.nextAt, &x.rule); err != nil {
			s.log.Error("scan schedule failed", "error", err)
			break
		}
		due = append(due, x)
	}
	rows.Close()
	for _, x := range due {
		if ctx.Err() != nil {
			return
		}
		claim, err := s.db.ExecContext(ctx, `UPDATE schedules SET status='running' WHERE id=? AND status='scheduled'`, x.id)
		if err != nil {
			s.log.Error("claim schedule failed", "schedule_id", x.id, "error", err)
			continue
		}
		claimed, _ := claim.RowsAffected()
		if claimed == 0 {
			continue
		}
		if err := s.execute(ctx, x.chatID, x.kind, x.prompt, x.fileID); err != nil {
			s.log.Error("schedule execution failed", "schedule_id", x.id, "error", err)
			var attempts int
			_ = s.db.QueryRowContext(ctx, `SELECT attempts FROM schedules WHERE id=?`, x.id).Scan(&attempts)
			attempts++
			if x.rule == "" && attempts < 3 {
				_, _ = s.db.ExecContext(ctx, `UPDATE schedules SET status='scheduled',attempts=?,last_error=?,next_at=? WHERE id=? AND status='running'`, attempts, err.Error(), time.Now().UTC().Add(5*time.Minute).Format(time.RFC3339Nano), x.id)
				continue
			}
			status := "running"
			if x.rule == "" {
				status = "failed"
			}
			_, _ = s.db.ExecContext(ctx, `UPDATE schedules SET status=?,attempts=?,last_error=? WHERE id=?`, status, attempts, err.Error(), x.id)
		}
		if x.rule != "" {
			if prev, perr := time.Parse(time.RFC3339Nano, x.nextAt); perr == nil {
				if nxt, nerr := reminders.NextOccurrence(prev, x.rule, s.loc); nerr == nil {
					for !nxt.After(time.Now()) {
						n2, e2 := reminders.NextOccurrence(nxt, x.rule, s.loc)
						if e2 != nil {
							break
						}
						nxt = n2
					}
					_, _ = s.db.ExecContext(ctx, `UPDATE schedules SET status='scheduled',next_at=?,last_error=NULL WHERE id=? AND status='running'`, nxt.UTC().Format(time.RFC3339Nano), x.id)
					continue
				}
			}
		}
		_, _ = s.db.ExecContext(ctx, `UPDATE schedules SET status='done' WHERE id=? AND status='running'`, x.id)
	}
}
func (s *Service) execute(ctx context.Context, chatID int64, kind, prompt string, fileID int64) error {
	switch kind {
	case "report":
		rctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		text, err := s.brain.RunReport(rctx, chatID, prompt)
		if err != nil {
			s.sendLong(ctx, chatID, "I couldn't generate the scheduled report: "+err.Error())
			return err
		}
		return s.sendLong(ctx, chatID, "📰 "+prompt+"\n\n"+text)
	case "message":
		return s.api.SendMessage(ctx, chatID, prompt)
	case "send_file":
		if s.files == nil {
			return s.api.SendMessage(ctx, chatID, "File storage is unavailable.")
		}
		if strings.TrimSpace(prompt) != "" {
			if err := s.api.SendMessage(ctx, chatID, prompt); err != nil {
				return err
			}
		}
		return s.files.SendToChat(ctx, fileID, chatID, s.tempDir, s.api)
	}
	return fmt.Errorf("unknown schedule kind %q", kind)
}

func (s *Service) sendLong(ctx context.Context, chatID int64, text string) error {
	const max = 3900
	for len(text) > 0 {
		if len(text) <= max {
			return s.api.SendMessage(ctx, chatID, text)
		}
		cut := max
		if i := strings.LastIndexByte(text[:max], '\n'); i > max/2 {
			cut = i
		}
		if err := s.api.SendMessage(ctx, chatID, text[:cut]); err != nil {
			return err
		}
		text = strings.TrimLeft(text[cut:], "\n")
	}
	return nil
}
