package router

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"secondbrain/internal/backup"
	"secondbrain/internal/config"
	"secondbrain/internal/convert"
	"secondbrain/internal/db"
	"secondbrain/internal/files"
	"secondbrain/internal/llm"
	"secondbrain/internal/notes"
	"secondbrain/internal/ntfy"
	"secondbrain/internal/reminders"
	"secondbrain/internal/search"
	"secondbrain/internal/stt"
	"secondbrain/internal/telegram"
)

type Router struct {
	cfg         config.Config
	db          *db.DB
	api         *telegram.Client
	log         *slog.Logger
	startedAt   time.Time
	reminders   *reminders.Service
	brain       *llm.Brain
	fileService *files.Service
	backups     *backup.Service
}

func New(cfg config.Config, database *db.DB, api *telegram.Client, reminderService *reminders.Service, brain *llm.Brain, fileService *files.Service, backupService *backup.Service, logger *slog.Logger) *Router {
	return &Router{cfg: cfg, db: database, api: api, log: logger, reminders: reminderService, brain: brain, fileService: fileService, backups: backupService, startedAt: time.Now()}
}
func AllowedUser(userID, allowed int64) bool { return userID != 0 && userID == allowed }

func (r *Router) Handle(ctx context.Context, u telegram.Update) {
	if u.CallbackQuery != nil {
		if !AllowedUser(u.CallbackQuery.From.ID, r.cfg.AllowedUserID) {
			r.log.Info("ignored callback", "callback_id", u.CallbackQuery.ID)
			return
		}
		encoded, _ := json.Marshal(u)
		id, err := r.db.LogInbox(ctx, "callback", string(encoded))
		if err != nil {
			r.log.Error("inbox write failed", "update_id", u.UpdateID, "error", err)
			return
		}
		err = r.handleCallback(ctx, *u.CallbackQuery)
		r.db.MarkInbox(ctx, id, err)
		if err != nil {
			r.log.Error("callback processing failed", "update_id", u.UpdateID, "error", err)
		}
		return
	}
	m := u.Message
	if m == nil {
		m = u.EditedMessage
	}
	if m == nil {
		return
	}
	if m.From == nil || !AllowedUser(m.From.ID, r.cfg.AllowedUserID) {
		r.log.Info("ignored update", "update_id", u.UpdateID)
		return
	}
	encoded, _ := json.Marshal(u)
	kind := "message"
	if m.Text != "" {
		kind = "text"
	}
	if m.Voice != nil {
		kind = "voice"
	}
	if m.Document != nil {
		kind = "file"
	}
	if len(m.Photo) > 0 {
		kind = "photo"
	}
	if m.Video != nil {
		kind = "video"
	}
	id, err := r.db.LogInbox(ctx, kind, string(encoded))
	if err != nil {
		r.log.Error("inbox write failed", "update_id", u.UpdateID, "error", err)
		return
	}
	if m.Voice != nil {
		err = r.handleVoice(ctx, m, id)
	} else if m.Document != nil || len(m.Photo) > 0 || m.Video != nil {
		err = r.handleFile(ctx, m)
	} else if m.Text == "" {
		r.db.MarkInbox(ctx, id, nil)
		return
	} else {
		if transcriptID, origin, ok := r.editingTranscript(ctx, m.Chat.ID); ok {
			if err = r.undoEffects(ctx, origin); err == nil {
				_, err = r.db.ExecContext(ctx, `UPDATE pending_transcripts SET text=?,status='corrected' WHERE id=?`, m.Text, transcriptID)
			}
			if err == nil {
				err = r.handleTextFromInbox(ctx, m, m.Text, origin)
			}
		} else {
			err = r.handleTextFromInbox(ctx, m, m.Text, 0)
		}
	}
	r.db.MarkInbox(ctx, id, err)
	if err != nil {
		r.log.Error("update processing failed", "update_id", u.UpdateID, "error", err)
	}
}

func (r *Router) handleTextFromInbox(ctx context.Context, m *telegram.Message, text string, originInboxID int64) error {
	text = strings.TrimSpace(text)
	lower := strings.ToLower(text)
	switch {
	case lower == "/start" || lower == "/help":
		return r.api.SendMessage(ctx, m.Chat.ID, "Second Brain Bot is running. Commands: /note <text>, /find <query>, /remind <when> | <text>, /reminders, /schedules, /task <text>, /tasks, /done <id>, /cancel <id>, /status, /usage. You can ask me to schedule a report, message, or file in natural language.")
	case strings.HasPrefix(lower, "/status"):
		return r.api.SendMessage(ctx, m.Chat.ID, r.status(ctx))
	case lower == "/forget" || lower == "/clear":
		if r.brain == nil {
			return r.api.SendMessage(ctx, m.Chat.ID, "Chat memory is not configured.")
		}
		if err := r.brain.ForgetHistory(ctx, m.Chat.ID); err != nil {
			return err
		}
		return r.api.SendMessage(ctx, m.Chat.ID, "🧹 Cleared our recent conversation memory.")
	case strings.HasPrefix(lower, "/find"):
		query := strings.TrimSpace(text[len("/find"):])
		if query == "" {
			return r.api.SendMessage(ctx, m.Chat.ID, "Usage: /find <query>")
		}
		return r.replySearch(ctx, m.Chat.ID, query)
	case lower == "/remind" || strings.HasPrefix(lower, "/remind "):
		return r.createReminderFrom(ctx, m.Chat.ID, strings.TrimSpace(text[len("/remind"):]), originInboxID)
	case lower == "/reminders":
		body, err := r.reminders.ListReminders(ctx)
		if err != nil {
			return err
		}
		return r.api.SendMessage(ctx, m.Chat.ID, body)
	case lower == "/schedules":
		if r.brain == nil {
			return r.api.SendMessage(ctx, m.Chat.ID, "Scheduled tasks are unavailable because the assistant is not configured.")
		}
		body, err := r.brain.ListSchedules(ctx, m.Chat.ID)
		if err != nil {
			return err
		}
		return r.api.SendMessage(ctx, m.Chat.ID, body)
	case lower == "/tasks":
		body, err := r.reminders.ListTasks(ctx)
		if err != nil {
			return err
		}
		return r.api.SendMessage(ctx, m.Chat.ID, body)
	case lower == "/files":
		if r.fileService == nil {
			return r.api.SendMessage(ctx, m.Chat.ID, "File storage is not configured.")
		}
		return r.api.SendMessageWithKeyboard(ctx, m.Chat.ID, "📂 Your files — choose a category:", filesCategoryKeyboard())
	case strings.HasPrefix(lower, "/tag "):
		if r.fileService == nil {
			return r.api.SendMessage(ctx, m.Chat.ID, "File storage is not configured.")
		}
		parts := strings.SplitN(strings.TrimSpace(text[len("/tag "):]), " ", 2)
		if len(parts) != 2 {
			return r.api.SendMessage(ctx, m.Chat.ID, "Usage: /tag <file id> <tags>")
		}
		id, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return r.api.SendMessage(ctx, m.Chat.ID, "Usage: /tag <file id> <tags>")
		}
		if err = r.fileService.Tag(ctx, id, strings.TrimSpace(parts[1])); err != nil {
			return err
		}
		return r.api.SendMessage(ctx, m.Chat.ID, "Updated file tags.")
	case strings.HasPrefix(lower, "/delete "):
		if r.fileService == nil {
			return r.api.SendMessage(ctx, m.Chat.ID, "File storage is not configured.")
		}
		id, err := strconv.ParseInt(strings.TrimSpace(text[len("/delete "):]), 10, 64)
		if err != nil {
			return r.api.SendMessage(ctx, m.Chat.ID, "Usage: /delete <file id>")
		}
		actionID, err := r.fileService.RequestDelete(ctx, id)
		if err != nil {
			return r.api.SendMessage(ctx, m.Chat.ID, "I couldn't find that file.")
		}
		kb := telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{{Text: "Delete permanently", CallbackData: fmt.Sprintf("delete:yes:%d", actionID)}, {Text: "Keep file", CallbackData: fmt.Sprintf("delete:no:%d", actionID)}}}}
		return r.api.SendMessageWithKeyboard(ctx, m.Chat.ID, "Confirm permanent file deletion?", kb)
	case strings.HasPrefix(lower, "/task "):
		id, err := r.reminders.AddTaskFrom(ctx, strings.TrimSpace(text[len("/task "):]), originInboxID)
		if err != nil {
			return err
		}
		return r.api.SendMessage(ctx, m.Chat.ID, fmt.Sprintf("Added task #%d.", id))
	case strings.HasPrefix(lower, "/done "):
		id, err := strconv.ParseInt(strings.TrimSpace(text[len("/done "):]), 10, 64)
		if err != nil {
			return r.api.SendMessage(ctx, m.Chat.ID, "Usage: /done <id>")
		}
		if err = r.reminders.Done(ctx, id); err != nil {
			return err
		}
		return r.api.SendMessage(ctx, m.Chat.ID, "Marked done.")
	case strings.HasPrefix(lower, "/cancel "):
		id, err := strconv.ParseInt(strings.TrimSpace(text[len("/cancel "):]), 10, 64)
		if err != nil {
			return r.api.SendMessage(ctx, m.Chat.ID, "Usage: /cancel <id>")
		}
		if err = r.reminders.Cancel(ctx, id); err != nil {
			return err
		}
		return r.api.SendMessage(ctx, m.Chat.ID, "Cancelled reminder.")
	case lower == "/usage":
		if r.brain == nil {
			return r.api.SendMessage(ctx, m.Chat.ID, "LLM is not configured.")
		}
		return r.api.SendMessage(ctx, m.Chat.ID, r.brain.Usage(ctx))
	case lower == "/backup":
		if r.backups == nil {
			return r.api.SendMessage(ctx, m.Chat.ID, "Encrypted backups are not configured.")
		}
		key, err := r.backups.Run(ctx)
		if err != nil {
			r.log.Error("manual backup failed", "error", err)
			return r.api.SendMessage(ctx, m.Chat.ID, "Backup failed. Check service logs.")
		}
		return r.api.SendMessage(ctx, m.Chat.ID, "Encrypted backup completed: "+key)
	case lower == "/testalarm":
		if r.cfg.NtfyURL == "" || r.cfg.NtfyToken == "" {
			return r.api.SendMessage(ctx, m.Chat.ID, "ntfy is not configured yet.")
		}
		client := ntfy.New(r.cfg.NtfyURL, r.cfg.NtfyTopic, r.cfg.NtfyToken)
		if err := client.Publish(ctx, "Second Brain test alarm", "This is a test notification.", "urgent"); err != nil {
			return r.api.SendMessage(ctx, m.Chat.ID, "ntfy test failed. Check the ntfy URL and token.")
		}
		return r.api.SendMessage(ctx, m.Chat.ID, "Test alarm sent to ntfy.")
	case strings.HasPrefix(lower, "/note "):
		body := strings.TrimSpace(text[len("/note "):])
		if body == "" {
			return r.api.SendMessage(ctx, m.Chat.ID, "Usage: /note <text>")
		}
		if _, err := notes.New(r.db).AddWithOrigin(ctx, body, noteSource(originInboxID), originInboxID); err != nil {
			return err
		}
		return r.api.SendMessage(ctx, m.Chat.ID, "Saved note.")
	default:
		if isLocalSearch(text) && !r.cfg.LLMSearchIntent {
			return r.replySearch(ctx, m.Chat.ID, searchRemainder(text))
		}
		if r.brain == nil {
			if _, err := notes.New(r.db).AddWithOrigin(ctx, text, noteSource(originInboxID), originInboxID); err != nil {
				return err
			}
			return r.api.SendMessage(ctx, m.Chat.ID, "Saved as a note. Configure the LLM to enable natural language actions.")
		}
		answer, err := r.brain.Handle(ctx, text, m.Chat.ID, originInboxID)
		if err != nil {
			r.log.Warn("LLM request failed; saved message as note", "error", err)
			if _, saveErr := notes.New(r.db).AddWithOrigin(ctx, text, noteSource(originInboxID), originInboxID); saveErr != nil {
				return saveErr
			}
			return r.api.SendMessage(ctx, m.Chat.ID, "I couldn't process that with the chat assistant, so I saved it as a note.")
		}
		if strings.TrimSpace(answer) == "" {
			return nil
		}
		return r.api.SendMessage(ctx, m.Chat.ID, answer)
	}
}

func isLocalSearch(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	for _, prefix := range []string{"search ", "find ", "look for ", "where is "} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}
func searchRemainder(text string) string {
	trimmed := strings.TrimSpace(text)
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{"search ", "find ", "look for ", "where is "} {
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(trimmed[len(prefix):])
		}
	}
	return trimmed
}

func (r *Router) replySearch(ctx context.Context, chatID int64, query string) error {
	results, mode, err := search.Query(ctx, r.db.DB, query, 10, 0)
	if err != nil {
		return r.api.SendMessage(ctx, chatID, "I couldn't parse that search. Use dates as YYYY-MM-DD.")
	}
	if len(results) == 0 {
		return r.api.SendMessage(ctx, chatID, "🔍 No matches found.")
	}
	var b strings.Builder
	noun := "results"
	if len(results) == 1 {
		noun = "result"
	}
	fmt.Fprintf(&b, "🔍 %d %s (%s match)\n", len(results), noun, mode)
	var keyboard [][]telegram.InlineKeyboardButton
	for i, item := range results {
		icon := "📝"
		if item.Kind == "file" {
			icon = "📄"
		}
		b.WriteString("\n")
		if item.Page > 0 {
			fmt.Fprintf(&b, "%d. %s %s · p.%d\n", i+1, icon, clip(item.Title, 44), item.Page)
		} else {
			fmt.Fprintf(&b, "%d. %s %s\n", i+1, icon, clip(item.Title, 44))
		}
		if s := strings.TrimSpace(cleanSnippet(item.Snippet)); s != "" {
			fmt.Fprintf(&b, "   %s\n", clip(s, 160))
		}
		if item.Kind == "file" {
			keyboard = append(keyboard, []telegram.InlineKeyboardButton{
				{Text: fmt.Sprintf("👁 Preview %d", i+1), CallbackData: fmt.Sprintf("file:preview:%d", item.RefID)},
				{Text: fmt.Sprintf("📤 Send %d", i+1), CallbackData: fmt.Sprintf("file:send:%d", item.RefID)},
			})
		}
	}
	answer := b.String()
	if len(answer) > 3900 {
		answer = answer[:3900]
	}
	if len(keyboard) > 0 {
		return r.api.SendMessageWithKeyboard(ctx, chatID, answer, telegram.InlineKeyboardMarkup{InlineKeyboard: keyboard})
	}
	return r.api.SendMessage(ctx, chatID, answer)
}
func shortName(name string) string {
	if len([]rune(name)) > 20 {
		return string([]rune(name)[:17]) + "…"
	}
	return name
}

func (r *Router) handleCallback(ctx context.Context, q telegram.CallbackQuery) error {
	if !AllowedUser(q.From.ID, r.cfg.AllowedUserID) {
		r.log.Info("ignored callback", "callback_id", q.ID)
		return nil
	}
	parts := strings.Split(q.Data, ":")
	if len(parts) == 3 && parts[0] == "confirm" {
		id, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return r.api.AnswerCallback(ctx, q.ID, "Invalid action")
		}
		if r.brain == nil {
			return r.api.AnswerCallback(ctx, q.ID, "LLM actions are disabled")
		}
		err = r.brain.ConfirmAction(ctx, id, parts[1] == "yes")
		if err != nil {
			return r.api.AnswerCallback(ctx, q.ID, "Confirmation expired or failed")
		}
		return r.api.AnswerCallback(ctx, q.ID, "Action updated")
	}
	if len(parts) == 3 && parts[0] == "voice" {
		id, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return r.api.AnswerCallback(ctx, q.ID, "Invalid transcript")
		}
		switch parts[1] {
		case "undo":
			err = r.undoTranscript(ctx, id, q.From.ID)
		case "edit":
			err = r.beginTranscriptEdit(ctx, id, q.From.ID)
		default:
			return r.api.AnswerCallback(ctx, q.ID, "Unknown action")
		}
		if err != nil {
			return r.api.AnswerCallback(ctx, q.ID, "Transcript action expired or failed")
		}
		if parts[1] == "edit" {
			return r.api.AnswerCallback(ctx, q.ID, "Send the corrected transcript as your next message")
		}
		return r.api.AnswerCallback(ctx, q.ID, "Voice action undone")
	}
	if len(parts) == 3 && parts[0] == "file" && parts[1] == "send" {
		id, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return r.api.AnswerCallback(ctx, q.ID, "Invalid file")
		}
		if r.fileService == nil {
			return r.api.AnswerCallback(ctx, q.ID, "File storage is unavailable")
		}
		if q.Message == nil {
			return r.api.AnswerCallback(ctx, q.ID, "File message is unavailable")
		}
		if err = r.fileService.SendToChat(ctx, id, q.Message.Chat.ID, r.cfg.TempDir, r.api); err != nil {
			_ = r.api.SendMessage(ctx, q.Message.Chat.ID, "I couldn't retrieve that file.")
			return r.api.AnswerCallback(ctx, q.ID, "File retrieval failed")
		}
		return r.api.AnswerCallback(ctx, q.ID, "File sent")
	}
	if len(parts) == 3 && parts[0] == "file" && parts[1] == "preview" {
		id, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return r.api.AnswerCallback(ctx, q.ID, "Invalid file")
		}
		return r.previewFile(ctx, q, id)
	}
	if parts[0] == "preview" && len(parts) >= 2 && parts[1] == "close" {
		if q.Message != nil {
			_ = r.api.DeleteMessage(ctx, q.Message.Chat.ID, q.Message.MessageID)
		}
		return r.api.AnswerCallback(ctx, q.ID, "Closed")
	}
	if parts[0] == "files" {
		return r.handleFilesCallback(ctx, q, parts)
	}
	if len(parts) == 3 && parts[0] == "delete" {
		id, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return r.api.AnswerCallback(ctx, q.ID, "Invalid confirmation")
		}
		if r.fileService == nil {
			return r.api.AnswerCallback(ctx, q.ID, "File storage is unavailable")
		}
		err = r.fileService.FinishDelete(ctx, id, parts[1] == "yes")
		if err != nil {
			return r.api.AnswerCallback(ctx, q.ID, "Delete failed or confirmation expired")
		}
		return r.api.AnswerCallback(ctx, q.ID, "File action complete")
	}
	if len(parts) != 3 || parts[0] != "reminder" {
		return r.api.AnswerCallback(ctx, q.ID, "Unknown action")
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return r.api.AnswerCallback(ctx, q.ID, "Invalid reminder")
	}
	switch parts[1] {
	case "done":
		err = r.reminders.Done(ctx, id)
	case "snooze10":
		err = r.reminders.Snooze(ctx, id, 10*time.Minute)
	case "snooze60":
		err = r.reminders.Snooze(ctx, id, time.Hour)
	default:
		return r.api.AnswerCallback(ctx, q.ID, "Unknown action")
	}
	if err != nil {
		return err
	}
	return r.api.AnswerCallback(ctx, q.ID, "Reminder updated")
}

func (r *Router) createReminderFrom(ctx context.Context, chatID int64, input string, origin int64) error {
	if input == "" {
		return r.api.SendMessage(ctx, chatID, "Usage: /remind [critical] <when> | <text> [| daily|weekly|monthly|cron expression]")
	}
	critical := false
	if strings.HasPrefix(strings.ToLower(input), "critical ") {
		critical = true
		input = strings.TrimSpace(input[len("critical "):])
	}
	parts := strings.Split(input, "|")
	if len(parts) < 2 || len(parts) > 3 {
		return r.api.SendMessage(ctx, chatID, "Use /remind <when> | <text> [| daily|weekly|monthly|cron expression]")
	}
	when, body := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	repeat := ""
	if len(parts) == 3 {
		repeat = strings.TrimSpace(parts[2])
	}
	id, due, err := r.reminders.AddReminderFrom(ctx, when, body, repeat, critical, origin)
	if err != nil {
		return r.api.SendMessage(ctx, chatID, "Couldn't create reminder: "+err.Error())
	}
	message := fmt.Sprintf("Reminder #%d set for %s.", id, due.In(r.reminders.Location()).Format("Mon Jan 2, 15:04"))
	if critical && (r.cfg.NtfyURL == "" || r.cfg.NtfyToken == "") {
		message += " ntfy is not configured, so phone alarm repeats are unavailable."
	}
	return r.api.SendMessage(ctx, chatID, message)
}

func noteSource(origin int64) string {
	if origin > 0 {
		return "voice"
	}
	return "text"
}
func (r *Router) handleVoice(ctx context.Context, m *telegram.Message, inboxID int64) error {
	if m.Voice == nil {
		return nil
	}
	if r.cfg.GroqAPIKey == "" {
		return r.api.SendMessage(ctx, m.Chat.ID, "Voice transcription is not configured yet; add GROQ_API_KEY to the service environment.")
	}
	if err := os.MkdirAll(r.cfg.TempDir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(r.cfg.TempDir, "voice-*.ogg")
	if err != nil {
		return err
	}
	path := file.Name()
	_ = file.Close()
	_ = os.Remove(path)
	_, err = r.api.DownloadFile(ctx, m.Voice.FileID, path, stt.MaxFreeRequestBytes)
	if err != nil {
		_ = r.api.SendMessage(ctx, m.Chat.ID, "I couldn't download that voice note. Please try sending it again.")
		return err
	}
	defer os.Remove(path)
	transcriber := stt.New(r.cfg.GroqAPIKey, r.cfg.STTModel, r.cfg.STTLanguage)
	transcript, err := transcriber.Transcribe(ctx, path, r.cfg.STTLanguage, r.cfg.TempDir)
	if err != nil {
		message := "I couldn't transcribe that voice note. Please try again."
		if apiErr, ok := err.(*stt.APIError); ok && apiErr.Status == 429 {
			message = "Voice transcription is rate-limited right now. Please wait a little and resend the voice note."
		}
		_ = r.api.SendMessage(ctx, m.Chat.ID, message)
		return err
	}
	res, err := r.db.ExecContext(ctx, `INSERT INTO pending_transcripts(user_id,chat_id,text,created_at,status,origin_inbox_id) VALUES(?,?,?,?, 'processed',?)`, m.From.ID, m.Chat.ID, transcript, time.Now().UTC().Format(time.RFC3339Nano), inboxID)
	if err != nil {
		return err
	}
	transcriptID, err := res.LastInsertId()
	if err != nil {
		return err
	}
	kb := telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{{Text: "Edit", CallbackData: fmt.Sprintf("voice:edit:%d", transcriptID)}, {Text: "Undo", CallbackData: fmt.Sprintf("voice:undo:%d", transcriptID)}}}}
	if err := r.api.SendMessageWithKeyboard(ctx, m.Chat.ID, "Heard: "+transcript, kb); err != nil {
		return err
	}
	return r.handleTextFromInbox(ctx, m, transcript, inboxID)
}
func (r *Router) handleFile(ctx context.Context, m *telegram.Message) error {
	if r.fileService == nil {
		return r.api.SendMessage(ctx, m.Chat.ID, "File storage is not configured yet; configure local storage or R2 with age encryption first.")
	}
	fileID, name, mimeType := "", "", ""
	var reportedSize int64
	switch {
	case m.Document != nil:
		fileID, name, mimeType, reportedSize = m.Document.FileID, m.Document.FileName, m.Document.MimeType, m.Document.FileSize
	case m.Video != nil:
		fileID, name, mimeType, reportedSize = m.Video.FileID, m.Video.FileName, m.Video.MimeType, m.Video.FileSize
		if name == "" {
			name = "video.mp4"
		}
		if mimeType == "" {
			mimeType = "video/mp4"
		}
	case len(m.Photo) > 0:
		p := m.Photo[len(m.Photo)-1]
		fileID, name, mimeType, reportedSize = p.FileID, "photo.jpg", "image/jpeg", p.FileSize
	}
	const maxFileSize = int64(200 << 20)
	if reportedSize > maxFileSize {
		return r.api.SendMessage(ctx, m.Chat.ID, "That file is over the 200 MB limit.")
	}
	if err := os.MkdirAll(r.cfg.TempDir, 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(r.cfg.TempDir, "incoming-*")
	if err != nil {
		return err
	}
	path := temp.Name()
	_ = temp.Close()
	_ = os.Remove(path)
	defer os.Remove(path)
	meta, err := r.api.DownloadFile(ctx, fileID, path, maxFileSize)
	if err != nil {
		return r.api.SendMessage(ctx, m.Chat.ID, "I couldn't download that file. If it is over 20 MB, the local Telegram Bot API server must be running.")
	}
	defer r.cleanupTelegramCache(meta.FilePath)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	caption := strings.TrimSpace(m.Caption)
	if target, ok := parseConvert(caption); ok {
		if cErr := r.handleConvert(ctx, m.Chat.ID, path, name, mimeType, target); cErr != nil {
			_ = r.api.SendMessage(ctx, m.Chat.ID, "Conversion failed: "+cErr.Error())
		}
		caption = ""
	}
	saved, err := r.fileService.Save(ctx, path, name, mimeType, caption)
	if err != nil {
		return r.api.SendMessage(ctx, m.Chat.ID, "I couldn't save that file to encrypted storage.")
	}
	if saved.Duplicate {
		return r.api.SendMessage(ctx, m.Chat.ID, fmt.Sprintf("That file is already saved as #%d (%s).", saved.ID, saved.Name))
	}
	messageID, err := r.api.SendMessageID(ctx, m.Chat.ID, fmt.Sprintf("Saved %s (%s). Indexing in background.", saved.Name, formatBytes(saved.Size)))
	if err != nil {
		return err
	}
	if err = r.fileService.SetProgressMessage(ctx, saved.ID, messageID); err != nil {
		r.log.Warn("save progress message id failed", "file_id", saved.ID, "error", err)
	}
	return nil
}
func (r *Router) cleanupTelegramCache(filePath string) {
	root := r.cfg.TelegramFileCacheDir
	if root == "" || !filepath.IsAbs(filePath) {
		return
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return
	}
	absoluteFile, err := filepath.Abs(filePath)
	if err != nil {
		return
	}
	rel, err := filepath.Rel(absoluteRoot, absoluteFile)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return
	}
	_ = os.Remove(absoluteFile)
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
func (r *Router) editingTranscript(ctx context.Context, chatID int64) (int64, int64, bool) {
	var id, origin int64
	err := r.db.QueryRowContext(ctx, `SELECT id,origin_inbox_id FROM pending_transcripts WHERE chat_id=? AND status='editing' AND created_at>=? ORDER BY id DESC LIMIT 1`, chatID, time.Now().Add(-10*time.Minute).UTC().Format(time.RFC3339Nano)).Scan(&id, &origin)
	return id, origin, err == nil
}
func (r *Router) beginTranscriptEdit(ctx context.Context, id, userID int64) error {
	var origin int64
	if err := r.db.QueryRowContext(ctx, `SELECT origin_inbox_id FROM pending_transcripts WHERE id=? AND user_id=? AND status IN ('processed','corrected')`, id, userID).Scan(&origin); err != nil {
		return err
	}
	if err := r.undoEffects(ctx, origin); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `UPDATE pending_transcripts SET status='editing' WHERE id=?`, id)
	return err
}
func (r *Router) undoTranscript(ctx context.Context, id, userID int64) error {
	var origin int64
	if err := r.db.QueryRowContext(ctx, `SELECT origin_inbox_id FROM pending_transcripts WHERE id=? AND user_id=? AND status IN ('processed','corrected','editing')`, id, userID).Scan(&origin); err != nil {
		return err
	}
	if err := r.undoEffects(ctx, origin); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `UPDATE pending_transcripts SET status='undone' WHERE id=?`, id)
	return err
}
func (r *Router) undoEffects(ctx context.Context, origin int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"search_fts", "search_tri"} {
		if _, err = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE kind='note' AND CAST(ref_id AS INTEGER) IN (SELECT id FROM notes WHERE origin_inbox_id=?)`, origin); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM notes WHERE origin_inbox_id=?`, origin); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE reminders SET status='cancelled' WHERE origin_inbox_id=? AND status IN ('scheduled','fired')`, origin); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM tasks WHERE origin_inbox_id=?`, origin); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE pending_actions SET status='rejected' WHERE origin_inbox_id=? AND status='pending'`, origin); err != nil {
		return err
	}
	return tx.Commit()
}
func (r *Router) status(ctx context.Context) string {
	var queue int
	_ = r.db.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE status='pending'`).Scan(&queue)
	var failed int
	_ = r.db.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE status='failed'`).Scan(&failed)
	var lastError string
	_ = r.db.QueryRowContext(ctx, `SELECT error FROM jobs WHERE status='failed' ORDER BY finished_at DESC LIMIT 1`).Scan(&lastError)
	if len(lastError) > 100 {
		lastError = lastError[:100] + "…"
	}
	if lastError == "" {
		lastError = "none"
	}
	var tokens int
	_ = r.db.QueryRowContext(ctx, `SELECT COALESCE(sum(prompt_tokens+completion_tokens),0) FROM llm_usage WHERE day=?`, time.Now().In(r.reminders.Location()).Format("2006-01-02")).Scan(&tokens)
	var upcoming string
	_ = r.db.QueryRowContext(ctx, `SELECT body FROM reminders WHERE status IN ('scheduled','fired') ORDER BY due_at LIMIT 1`).Scan(&upcoming)
	if upcoming == "" {
		upcoming = "none"
	}
	return fmt.Sprintf("Second Brain Bot\nUptime: %s\nAvailable RAM: %s\nAvailable swap: %s\nDisk: %s\nQueued jobs: %d\nFailed jobs: %d (latest: %s)\nLLM tokens today: %d\nNext reminder: %s", time.Since(r.startedAt).Round(time.Second), readMem("MemAvailable"), readMem("SwapFree"), diskUsage(r.cfg.DataDir), queue, failed, lastError, tokens, upcoming)
}

func filesCategoryKeyboard() telegram.InlineKeyboardMarkup {
	return telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{
		{{Text: "📷 Photos", CallbackData: "files:cat:photo"}, {Text: "📄 Documents", CallbackData: "files:cat:document"}},
		{{Text: "🎬 Videos", CallbackData: "files:cat:video"}, {Text: "💻 Code", CallbackData: "files:cat:code"}},
		{{Text: "🗂 Other", CallbackData: "files:cat:other"}},
	}}
}

var categoryTitles = map[string]string{"photo": "📷 Photos", "document": "📄 Documents", "video": "🎬 Videos", "code": "💻 Code", "other": "🗂 Other"}

func (r *Router) handleFilesCallback(ctx context.Context, q telegram.CallbackQuery, parts []string) error {
	if r.fileService == nil || q.Message == nil {
		return r.api.AnswerCallback(ctx, q.ID, "Unavailable")
	}
	chatID, msgID := q.Message.Chat.ID, q.Message.MessageID
	if len(parts) >= 2 && parts[1] == "home" {
		_ = r.api.EditMessageKeyboard(ctx, chatID, msgID, "📂 Your files — choose a category:", filesCategoryKeyboard())
		return r.api.AnswerCallback(ctx, q.ID, "")
	}
	if len(parts) == 3 && parts[1] == "cat" {
		category := parts[2]
		rows, err := r.fileService.List(ctx, category, 8)
		if err != nil {
			return r.api.AnswerCallback(ctx, q.ID, "Couldn't list files")
		}
		title := categoryTitles[category]
		if title == "" {
			title = "Files"
		}
		var b strings.Builder
		if len(rows) == 0 {
			fmt.Fprintf(&b, "%s\n\nNo files here yet.", title)
		} else {
			fmt.Fprintf(&b, "%s", title)
			for i, f := range rows {
				fmt.Fprintf(&b, "\n\n%d. %s\n   %s · %s", i+1, clip(f.Name, 44), formatBytes(f.Size), f.Status)
			}
		}
		var kb [][]telegram.InlineKeyboardButton
		for i, f := range rows {
			kb = append(kb, []telegram.InlineKeyboardButton{
				{Text: fmt.Sprintf("👁 Preview %d", i+1), CallbackData: fmt.Sprintf("file:preview:%d", f.ID)},
				{Text: fmt.Sprintf("📤 Send %d", i+1), CallbackData: fmt.Sprintf("file:send:%d", f.ID)},
			})
		}
		kb = append(kb, []telegram.InlineKeyboardButton{{Text: "⬅ Back", CallbackData: "files:home"}})
		_ = r.api.EditMessageKeyboard(ctx, chatID, msgID, b.String(), telegram.InlineKeyboardMarkup{InlineKeyboard: kb})
		return r.api.AnswerCallback(ctx, q.ID, "")
	}
	return r.api.AnswerCallback(ctx, q.ID, "Unknown action")
}

// MORE_ROUTER_HELPERS
func (r *Router) previewFile(ctx context.Context, q telegram.CallbackQuery, id int64) error {
	if r.fileService == nil || q.Message == nil {
		return r.api.AnswerCallback(ctx, q.ID, "Unavailable")
	}
	path, cleanup, err := r.fileService.PreviewImage(ctx, id, r.cfg.TempDir)
	if err != nil {
		return r.api.AnswerCallback(ctx, q.ID, "No preview for this file")
	}
	defer cleanup()
	kb := telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{{Text: "✕ Close", CallbackData: "preview:close"}}}}
	if err := r.api.SendPhotoWithKeyboard(ctx, q.Message.Chat.ID, path, "", kb); err != nil {
		r.log.Warn("preview send failed", "file_id", id, "error", err)
		return r.api.AnswerCallback(ctx, q.ID, "Preview failed")
	}
	return r.api.AnswerCallback(ctx, q.ID, "")
}

func clip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

func cleanSnippet(s string) string { return strings.Join(strings.Fields(s), " ") }

// parseConvert detects a conversion request in an upload caption, e.g. "convert to pdf",
// "covert this to pdf" (typo-tolerant), or a caption beginning "to pdf" / "as docx".
func parseConvert(caption string) (string, bool) {
	l := strings.ToLower(strings.TrimSpace(caption))
	if l == "" {
		return "", false
	}
	hasVerb := strings.Contains(l, "convert") || strings.Contains(l, "covert") || strings.Contains(l, "conver")
	hasPrefix := false
	for _, p := range []string{"to pdf", "to docx", "to word", "to document", "to doc", "as pdf", "as docx", "as word", "into pdf", "into docx", "into word"} {
		if strings.HasPrefix(l, p) {
			hasPrefix = true
			break
		}
	}
	if !hasVerb && !hasPrefix {
		return "", false
	}
	switch {
	case strings.Contains(l, "pdf"):
		return "pdf", true
	case strings.Contains(l, "docx") || strings.Contains(l, "word") || strings.Contains(l, "document") || strings.Contains(l, "doc"):
		return "docx", true
	}
	return "", false
}

func (r *Router) handleConvert(ctx context.Context, chatID int64, srcPath, name, mime, target string) error {
	lower := strings.ToLower(name)
	isPDF := mime == "application/pdf" || strings.HasSuffix(lower, ".pdf")
	isDocx := strings.HasSuffix(lower, ".docx") || strings.Contains(mime, "word") || strings.Contains(mime, "document")
	base := strings.TrimSuffix(name, filepath.Ext(name))
	if strings.TrimSpace(base) == "" {
		base = "converted"
	}
	if err := os.MkdirAll(r.cfg.TempDir, 0700); err != nil {
		return err
	}
	switch target {
	case "pdf":
		if isPDF {
			return fmt.Errorf("that's already a PDF")
		}
		if !isDocx {
			return fmt.Errorf("I can only convert Word (.docx) files to PDF")
		}
		out := filepath.Join(r.cfg.TempDir, base+".pdf")
		defer os.Remove(out)
		converted := false
		if convert.GraphEnabled(r.cfg.MSGraphTokenFile) {
			if err := convert.GraphDocxToPDF(ctx, r.cfg.MSGraphTokenFile, srcPath, out, filepath.Ext(name)); err != nil {
				r.log.Warn("graph docx->pdf failed; using local fallback", "error", err)
			} else {
				converted = true
			}
		}
		if !converted {
			if err := convert.DocxToPDF(srcPath, out); err != nil {
				return err
			}
		}
		return r.api.SendFile(ctx, chatID, out, base+".pdf", "application/pdf")
	case "docx":
		if isDocx {
			return fmt.Errorf("that's already a Word document")
		}
		if !isPDF {
			return fmt.Errorf("I can only convert PDF files to Word (.docx)")
		}
		out := filepath.Join(r.cfg.TempDir, base+".docx")
		defer os.Remove(out)
		if err := convert.PDFToDocx(srcPath, out); err != nil {
			return err
		}
		return r.api.SendFile(ctx, chatID, out, base+".docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	}
	return fmt.Errorf("unsupported conversion target")
}
