package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"secondbrain/internal/backup"
	"secondbrain/internal/config"
	"secondbrain/internal/db"
	"secondbrain/internal/files"
	"secondbrain/internal/llm"
	"secondbrain/internal/monitor"
	"secondbrain/internal/ntfy"
	"secondbrain/internal/ocr"
	"secondbrain/internal/reminders"
	"secondbrain/internal/router"
	"secondbrain/internal/scheduler"
	"secondbrain/internal/storage"
	"secondbrain/internal/telegram"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration invalid", "error", err)
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if len(os.Args) > 1 && os.Args[1] == "--restore" {
		if len(os.Args) != 4 {
			logger.Error("usage: secondbrain --restore <object-key> <new-database-path>")
			os.Exit(2)
		}
		store, restoreErr := storage.New(ctx, cfg)
		if restoreErr != nil {
			logger.Error("storage startup failed", "error", restoreErr)
			os.Exit(1)
		}
		if store == nil {
			logger.Error("restore requires file storage")
			os.Exit(2)
		}
		if restoreErr = backup.Restore(ctx, store, os.Args[2], os.Args[3]); restoreErr != nil {
			logger.Error("restore failed", "error", restoreErr)
			os.Exit(1)
		}
		logger.Info("restore verified", "path", os.Args[3])
		return
	}
	database, err := db.Open(ctx, cfg.DataDir)
	if err != nil {
		logger.Error("database startup failed", "error", err)
		os.Exit(1)
	}
	defer database.Close()
	api := telegram.New(cfg.TelegramToken, cfg.TelegramAPIBase, cfg.HTTPTimeout)
	reminderService, err := reminders.New(database, api, cfg.Timezone, cfg.AllowedUserID, logger)
	if err != nil {
		logger.Error("reminder startup failed", "error", err)
		os.Exit(1)
	}
	if cfg.NtfyURL != "" {
		reminderService.SetNotifier(ntfy.New(cfg.NtfyURL, cfg.NtfyTopic, cfg.NtfyToken))
	}
	var fileService *files.Service
	var backupService *backup.Service
	store, storageErr := storage.New(ctx, cfg)
	if storageErr != nil {
		logger.Error("storage startup failed", "error", storageErr)
		os.Exit(1)
	}
	if store != nil {
		fileService = files.New(database, store)
		backupService = backup.New(database, store, cfg.DataDir, cfg.TempDir)
		if len(os.Args) > 1 && os.Args[1] == "--backup" {
			key, backupErr := backupService.Run(ctx)
			if backupErr != nil {
				logger.Error("backup command failed", "error", backupErr)
				os.Exit(1)
			}
			logger.Info("backup completed", "object_key", key)
			return
		}
		worker := ocr.NewWorker(cfg, database, store, api, cfg.AllowedUserID, logger)
		go worker.Run(ctx)
	} else if len(os.Args) > 1 && os.Args[1] == "--backup" {
		logger.Error("backup requires file storage configuration")
		os.Exit(2)
	}
	go reminderService.Run(ctx)
	var notifier *ntfy.Client
	if cfg.NtfyURL != "" {
		notifier = ntfy.New(cfg.NtfyURL, cfg.NtfyTopic, cfg.NtfyToken)
		go monitor.Run(ctx, cfg.DataDir, database, notifier, logger)
	}
	brain := llm.NewBrain(cfg, database, api, reminderService, fileService)
	scheduleService := scheduler.New(database, api, brain, fileService, reminderService.Location(), cfg.TempDir, logger)
	go scheduleService.Run(ctx)
	handler := router.New(cfg, database, api, reminderService, brain, fileService, backupService, logger)
	// Best effort only: a failed menu update must never stop the bot.
	// This gives Telegram's built-in command list without BotFather steps.
	_ = api.SetMyCommands(ctx, []telegram.BotCommand{
		{Command: "menu", Description: "Quick actions"},
		{Command: "today", Description: "Today's reminders and open tasks"},
		{Command: "note", Description: "Save a note"},
		{Command: "find", Description: "Search notes and files locally"},
		{Command: "remind", Description: "Set a reminder"},
		{Command: "reminders", Description: "Upcoming reminders"},
		{Command: "schedules", Description: "Scheduled reports and messages"},
		{Command: "task", Description: "Add a task"},
		{Command: "tasks", Description: "Open tasks"},
		{Command: "done", Description: "Mark a reminder or task done"},
		{Command: "cancel", Description: "Cancel a reminder"},
		{Command: "files", Description: "Browse saved files"},
		{Command: "status", Description: "Bot and VM status"},
		{Command: "usage", Description: "LLM tokens today"},
		{Command: "backup", Description: "Run an encrypted backup"},
		{Command: "forget", Description: "Clear recent chat memory"},
		{Command: "help", Description: "Show help and quick actions"},
	})
	var offset int64
	for ctx.Err() == nil {
		updates, err := api.GetUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			logger.Warn("telegram polling failed", "error", err)
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
			}
			continue
		}
		for _, update := range updates {
			handler.Handle(ctx, update)
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
		}
	}
}
