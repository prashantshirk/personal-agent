# Second Brain Bot

A self-hosted single-user Telegram assistant built in Go. It stores notes, reminders and tasks; schedules recurring research reports, messages, and stored-file sends; keeps encrypted files locally or in R2; indexes PDF/image text locally with SQLite FTS5; transcribes optional voice notes; and can use an OpenAI-compatible chat endpoint and Firecrawl web search.

Create scheduled tasks in natural language (for example, “Every day at 7am, send me an AI news report” or “Send file #8 tomorrow at 7pm with a note to fill it out”). Use `/schedules` to list them, then ask the assistant to cancel a schedule by its ID. Reports use the configured LLM and Firecrawl daily limits.

## Project docs

- [Full design and source of truth](docs/DESIGN.md)
- [Deployment and VM preparation](docs/DEPLOYMENT.md)
- [VM operations: access, data, deploy, stop, remove](docs/VM.md)
- [Restore runbook](docs/RUNBOOK.md)
- [Operations and recovery](docs/OPERATIONS.md)
- [Acceptance checklist and run status](docs/ACCEPTANCE.md)

## Build and tests

Requires Go 1.22+. Run `go test ./...` and `go build ./cmd/secondbrain`. Build the VM binary from a machine with Go using `GOOS=linux GOARCH=amd64 go build -o secondbrain ./cmd/secondbrain` (choose the actual VM architecture).

The bot requires `TELEGRAM_BOT_TOKEN`, `TELEGRAM_ALLOWED_USER_ID`, Gemini settings, and `GROQ_API_KEY`. Chat tries Gemini 3.5 Flash-Lite, Gemini 3.1 Flash-Lite, then Groq GPT-OSS 20B; Groq Whisper Large v3 handles voice transcription. Keep service secrets in `/etc/secondbrain/env` on the VM and out of chat, source files, and logs. A safe template is in [`deploy/secondbrain.env.example`](deploy/secondbrain.env.example).

For deployments, `.github/workflows/deploy.yml` builds the Linux amd64 executable and reaches the private VM through Tailscale. After the one-time setup in [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md), pushes to `main` deploy automatically; the VM retains its secrets and database.

`go build ./...`, `go vet ./...`, and the Linux/amd64 cross-build passed. Focused tests passed for database, LLM, reminders, router, Firecrawl, storage, and ntfy; Windows Application Control blocked launching the search test executable. Stage 4 is deployed to the VM; end-to-end Telegram scheduling/report checks remain in `docs/ACCEPTANCE.md`.
