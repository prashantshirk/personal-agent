# Second Brain Bot — Design & Build Guide

> **Correction — 2026-10-26: The database is SQLite, not Cloudflare R2.** R2 was never the database; it was only a possible file-object store. On the deployed VM the active file storage is local: `FILE_STORAGE_BACKEND=local`, age-encrypted objects under `/var/lib/secondbrain/data/objects/`, and the SQLite database at `/var/lib/secondbrain/data/secondbrain.db`. R2 is an optional alternative backend (`FILE_STORAGE_BACKEND=r2`) only. `docs/DEPLOYMENT.md` and `docs/VM.md` already said this; this older design document is now corrected to match the code in `internal/storage/` and `internal/config/`.

A private, self-hosted personal assistant you talk to on Telegram. You send it text, voice notes, photos, videos and PDFs. It keeps notes, runs reminders and tasks, stores your files, and finds them again by searching the text inside them. It runs on a small Ubuntu VM (2 vCPU, 1 GB RAM, 30 GB disk).

How to use this document:

1. Save it in your project repo as `docs/DESIGN.md`.
2. Open your coding agent in that repo and paste **Prompt 0** (Section 12).
3. Go phase by phase (Section 13). Each phase has a prompt, and each ends with a checklist you verify before moving on.
4. When a phase needs an API key or account, use the matching **"Explain it to me"** prompt (Section 14). The agent will walk you through it step by step.

---

## 1. Goals and non-goals

**Goals**

- Message the bot in plain language or by voice. No need to type commands for everyday use.
- Notes, tasks, one-off and recurring reminders.
- Critical reminders that keep alerting you until you acknowledge them.
- Store photos, videos and PDFs (each up to about 200 MB) and get them back later.
- Find files by searching the words inside them (OCR for scans and for text inside images).
- Web lookups on request.
- Fit comfortably on a 1 GB VM and cost close to nothing.

**Non-goals (deliberately not built)**

- No embeddings or vector search.
- No AI model ever sees the contents of your files, photos or documents.
- No phone calls (replaced by alarm-style push notifications through ntfy).
- No local LLM, no browser automation, no multi-user support.

---

## 2. Decisions

### Confirmed by you

| Decision | Choice |
|---|---|
| Interface | Telegram bot |
| Host | Ubuntu VM, 2 vCPU, 1 GB RAM, 30 GB disk |
| File storage | Local age-encrypted storage on the VM (photos, videos, PDFs up to about 200 MB); Cloudflare R2 is an optional alternative backend, not the default |
| Voice input | Yes, voice notes are first-class input |
| File search | Keyword full-text search over locally OCR'd text. No embeddings, no LLM reading documents |
| Critical alerts | Self-hosted ntfy |
| Web search | Firecrawl |

### Defaults I chose (tell your agent if you want to change any)

| Topic | Default | Why |
|---|---|---|
| Language | Go, single static binary | Small RAM footprint, easy deploy. Python works too, the design is identical |
| Transcription | Groq Whisper large-v3 | Free tier, no card, OpenAI-compatible |
| LLM for chat intent | Any OpenAI-compatible endpoint, configured by env vars | Provider is still open (see Section 17) |
| Search commands | `/find ...` and messages starting with "search" or "find" are handled **locally, without any LLM** | Keeps search phrases private and free |
| File encryption | Encrypt every file on the VM with `age` before it is stored | If R2 is the configured backend, Cloudflare then only stores ciphertext |
| Timezone | `Asia/Kolkata` (configurable) | Reminders must resolve "tomorrow at 6" correctly |
| Exposing ntfy | Cloudflare Tunnel, no open inbound ports | Easy TLS, nothing to port-forward |
| SQLite driver | `modernc.org/sqlite` (pure Go) | No cgo, easy cross-compiling |

---

## 3. Architecture

```
You (Telegram app)
      |
      v
Telegram servers  <----------------------------------+
      |                                               |
      v                                               |
telegram-bot-api (local mode, 127.0.0.1:8081)         |
      |                                               |
      v                                               |
+---------------------- secondbrain (Go binary) -----------------------+
|  router -> commands | local search | file pipeline | LLM brain        |
|                                                                       |
|  SQLite (notes, reminders, tasks, files, FTS5 index, jobs, inbox)     |
|  scheduler loop      job worker (OCR/index)      backup timer         |
+--+------------+---------------+-----------------+--------------+-----+
   |            |               |                 |              |
   v            v               v                 v              v
 Groq         LLM API       Local encrypted   Firecrawl        ntfy (self-hosted)
 (voice)      (intent)      storage (R2 opt.) (web search)     via Cloudflare Tunnel
                                                                   |
                                                                   v
                                                             your phone alarm
```

Local tools used by the worker: `pdftotext`, `pdftoppm`, `pdfimages` (poppler-utils), `tesseract`, ImageMagick, `ffmpeg` (voice conversion only if needed).

### Message router (the heart of the bot)

Every incoming update goes through the same steps, in order:

1. **Allowlist.** If the sender's user ID is not yours, ignore it silently.
2. **Inbox log.** Append the raw message to an `inbox` table before doing anything else, so nothing is ever lost, even if later steps fail.
3. **Commands** (`/note`, `/find`, `/remind`, `/tasks`, `/status`, ...) are handled by deterministic code.
4. **Files** (document, photo, video) go to the file pipeline. The caption becomes the file's tags and description.
5. **Voice notes** are transcribed. The bot replies with the transcript, then treats it as text from step 6 onward.
6. **Local search trigger.** If the text matches `^(search|find|look for|where is)\b`, run the local search. No LLM involved.
7. **Everything else** goes to the LLM brain with tools. If the LLM is down, over budget or confused, the message is saved as a note. Input is never dropped.

---

## 4. Features in detail

### 4.1 Telegram interface
- Long polling, so no webhook, domain or open port is needed for the bot itself.
- Only your user ID gets responses.
- Real command menu (set through BotFather), inline buttons on reminders (Done, Snooze 10 min, Snooze 1 h) and on search results (Send file).
- Cost: free. VM load: tiny.

### 4.2 LLM brain (natural language to actions)
- Receives only your short message plus tool definitions and the current date, time and timezone.
- Tools: `add_note`, `search_notes`, `set_reminder`, `list_reminders`, `cancel_reminder`, `add_task`, `list_tasks`, `complete_task`, `web_search`.
- **Direct-reply tools:** `search_notes` and any file search send their results straight to Telegram. The result text is never fed back to the model, so the model never sees your note or document contents.
- Destructive actions (delete, cancel) ask for confirmation with a button.
- Daily token budget with a hard stop. `/usage` shows spend.
- Cost: cents per month at personal volume, or free tiers with limits. VM load: tiny (HTTP calls).

### 4.3 Notes
- Stored in SQLite and indexed in FTS5. Send plain text or a voice note and it is saved.
- Anything that is not clearly a reminder, task, search or question is saved as a note by default.

### 4.4 Voice notes
- Telegram voice messages are small OGG/Opus files.
- Flow: download, send to Groq Whisper, get transcript, echo it back ("Heard: ..."), route it like typed text.
- Every transcript reply has an **Edit** button and an **Undo** button, because a misheard time on a reminder is the worst failure.
- Language: configurable hint (`en`, `hi`, `mr`, or auto). Test with your real speech before trusting it. Mixed-language sentences can come out in mixed scripts.
- If Groq rejects the OGG file, convert with ffmpeg to 16 kHz mono FLAC or MP3 first.
- Voice notes sent within about 90 seconds after a file become that file's caption and tags.
- Cost: free tier (limits can change). Note that audio leaves the VM for Groq.

### 4.5 Reminders and tasks
- Stored in UTC with your timezone for display. Supports one-off, daily, weekly, monthly and cron-style repeats.
- Scheduler: a goroutine ticking every 20 seconds, selecting due rows. Survives restarts because state is in SQLite.
- After downtime, overdue reminders fire immediately marked "(late)".
- Each reminder message has Done, Snooze 10 min, Snooze 1 h buttons.
- Free. VM load: tiny.

### 4.6 Critical alarms (ntfy)
- A reminder tagged critical also sends a push through your **self-hosted ntfy** with the `urgent` priority.
- The bot repeats every 5 minutes (up to 12 times by default) until you tap Done in Telegram.
- The repeat-until-acknowledged logic lives in the bot, not in ntfy.
- Android can let the max-priority notification channel override Do Not Disturb. On iOS, ntfy cannot force a critical alert, so you may need to allow the ntfy app through your Focus settings, and iOS self-hosted instant delivery needs the upstream relay option.
- Cost: free. VM load: about 20-40 MB for ntfy, plus the tunnel process.

### 4.7 File storage
- You send files as **File/Document** to keep originals (Telegram compresses photos and videos sent normally).
- A **local Telegram Bot API server** lifts the 20 MB download limit so files up to about 200 MB work.
- Pipeline: hash, dedupe, encrypt with `age` while streaming, save to the active storage backend (local file under `/var/lib/secondbrain/data/objects/` on the deployed VM; multipart upload to R2 only when `FILE_STORAGE_BACKEND=r2`), row in `files` table.
- Local storage uses VM disk, so watch disk usage (see section 7.3). The R2 free tier was about 10 GB at the time of writing; that only matters if you switch to the R2 backend. Check Cloudflare's current limits and whether it requires a payment method on file.

### 4.8 Indexing (OCR) in the background
- One job at a time. Page by page. Checkpointed, so a crash resumes.
- Text PDFs: `pdftotext` per page.
- Pages with little or no text: rasterize that page at 300 DPI, clean up, run Tesseract.
- Pages that contain embedded images: optionally OCR them as well and merge the text, so words inside pictures inside PDFs are found.
- Photos and screenshots: Tesseract directly.
- Videos: stored but not indexed (search by caption and tags). Optional later: extract and compress the audio with ffmpeg and transcribe.
- All local. Free. Heavy but temporary.

### 4.9 Search
- One SQLite FTS5 index covers notes and file pages.
- BM25 ranking with weights: title and tags count more than body.
- Operators: phrases (`"security deposit"`), prefix (`depos*`), filters like `type:pdf`, `after:2026-09-01`.
- Strategy: try AND first, fall back to OR, then fall back to a trigram index for partial words and OCR typos.
- Results show file name, page number and a highlighted snippet, with a **Send file** button.
- Free. VM load: light.

### 4.10 File retrieval
- Tap **Send file**: the bot streams the object from the active storage backend (local objects directory on the deployed VM, or R2 if that backend is configured), decrypts it, and sends it with `sendDocument`, `sendPhoto` or `sendVideo`.
- Presigned links are not used because the files are encrypted.

### 4.11 Web search (Firecrawl)
- Tool `web_search(query)` calls `POST https://api.firecrawl.dev/v2/search` with your API key and a small `limit` (3-5), titles and snippets only.
- Results go back to the LLM to summarize. Treat all web content as untrusted (see Section 9).

### 4.12 Operations
- `/status`: RAM, swap, disk %, job queue length, last errors, LLM tokens today, upcoming reminders.
- Nightly encrypted backup of the SQLite database to the active storage backend under `backups/` (on the deployed VM: `/var/lib/secondbrain/data/objects/backups/`).
- ntfy warning when disk is above 80% or jobs keep failing.

---

## 5. Privacy model: what leaves the VM

| Data | Goes to | Notes |
|---|---|---|
| Everything you send the bot | Telegram servers | Bot chats are not end-to-end encrypted |
| Voice audio | Groq | For transcription |
| Short chat text (reminders, tasks, general messages) | LLM provider | Never file contents, never search results |
| Search phrases starting with "search"/"find" | Nobody | Handled locally |
| Files | Active storage backend: local VM disk on the deployed VM (R2 only if `FILE_STORAGE_BACKEND=r2`) | Encrypted with `age` first, so any external store sees ciphertext only |
| OCR text and search index | Stays on the VM | Also in the encrypted DB backups |
| Web search queries | Firecrawl | Only when you ask for a web lookup |

---

## 6. Tech stack

| Layer | Choice |
|---|---|
| Language | Go 1.22 or newer |
| Telegram library | `github.com/go-telegram/bot` (or `telego`). Agent must verify it supports a custom API server URL and file uploads |
| Database | SQLite with FTS5 via `modernc.org/sqlite` |
| Recurrence | `github.com/robfig/cron/v3` parser for cron-style repeats |
| R2 / S3 (optional backend only) | `github.com/aws/aws-sdk-go-v2` with the S3 uploader (multipart) |
| Encryption | `filippo.io/age` streaming encrypt and decrypt |
| Unicode | `golang.org/x/text/unicode/norm` |
| HTTP clients | Standard library for Groq, LLM and Firecrawl (all plain JSON/HTTP) |
| PDF / OCR | `poppler-utils`, `tesseract-ocr` (+ language packs), ImageMagick |
| Process mgmt | systemd |
| Tunnel | `cloudflared` |
| Push alerts | `ntfy` server |
| Telegram local API | `aiogram/telegram-bot-api` Docker image (or a prebuilt binary) |

Build on your laptop and copy the binary to the VM (`GOOS=linux GOARCH=<vm arch> go build`). Compiling Go on a 1 GB VM is slow and may run out of memory. Check the VM architecture with `uname -m`.

---

## 7. VM preparation and resource budget

### 7.1 One-time setup commands

```bash
# Swap (safety net for OCR spikes)
sudo fallocate -l 2G /swapfile
sudo chmod 600 /swapfile
sudo mkswap /swapfile
sudo swapon /swapfile
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
echo 'vm.swappiness=20' | sudo tee /etc/sysctl.d/99-swap.conf
sudo sysctl --system

# Dedicated user and directories
sudo useradd --system --create-home --home-dir /var/lib/secondbrain --shell /usr/sbin/nologin secondbrain
sudo mkdir -p /etc/secondbrain /var/lib/secondbrain/{data,tmp} /opt/secondbrain
sudo chown -R secondbrain:secondbrain /var/lib/secondbrain /opt/secondbrain
sudo chmod 750 /etc/secondbrain

# Packages
sudo apt update
sudo apt install -y poppler-utils tesseract-ocr tesseract-ocr-eng \
  tesseract-ocr-hin tesseract-ocr-mar imagemagick sqlite3 ffmpeg \
  ufw curl ca-certificates unattended-upgrades

# Firewall: only SSH inbound (the tunnel needs no inbound ports)
sudo ufw allow OpenSSH
sudo ufw enable
```

### 7.2 Memory budget (estimates, measure with `free -m`)

| Component | Approx. RAM |
|---|---|
| Ubuntu baseline | 150-250 MB |
| secondbrain (Go) | 30-80 MB |
| telegram-bot-api (+ Docker daemon if used) | 50-250 MB |
| ntfy | 20-40 MB |
| cloudflared | 20-40 MB |
| Tesseract during a page (temporary) | 200-400 MB peak |
| pdftoppm during a page (temporary) | about 100 MB peak |

It fits, tightly. Rules that keep it safe:

- Only one OCR job at a time, one page at a time.
- Set `OMP_THREAD_LIMIT=1` for Tesseract.
- Set `GOMEMLIMIT` (for example `200MiB`) for the Go process.
- Set systemd `MemoryHigh` and `MemoryMax` on the bot service (children such as Tesseract count against it).
- Subtract anything else already running on the VM.

### 7.3 Disk budget
Text, FTS indexes and metadata for thousands of documents take well under 1 GB. With the local backend active on the deployed VM, files live on the VM disk under `/var/lib/secondbrain/data/objects/`, not in R2. Watch disk usage, the temp directory and the Telegram local API server's cache (it keeps downloaded files until you delete them, so the bot must clean up after each file).

---

## 8. Data model (SQLite)

The agent should treat this as a starting point and add indexes and migrations.

```sql
-- every incoming message, before any processing
CREATE TABLE inbox (
  id INTEGER PRIMARY KEY, received_at TEXT NOT NULL,
  kind TEXT NOT NULL,            -- text|voice|file|photo|video|callback
  raw TEXT, handled INTEGER DEFAULT 0, error TEXT
);

CREATE TABLE notes (
  id INTEGER PRIMARY KEY, created_at TEXT NOT NULL,
  body TEXT NOT NULL, tags TEXT DEFAULT '', source TEXT DEFAULT 'text'  -- text|voice
);

CREATE TABLE tasks (
  id INTEGER PRIMARY KEY, created_at TEXT NOT NULL,
  body TEXT NOT NULL, due_at TEXT, status TEXT DEFAULT 'open', done_at TEXT
);

CREATE TABLE reminders (
  id INTEGER PRIMARY KEY, body TEXT NOT NULL,
  due_at TEXT NOT NULL,                 -- UTC ISO8601
  repeat_rule TEXT,                     -- null | 'daily' | 'weekly' | cron expr
  critical INTEGER DEFAULT 0,
  status TEXT DEFAULT 'scheduled',      -- scheduled|fired|acked|cancelled
  next_attempt_at TEXT, attempts INTEGER DEFAULT 0,
  acked_at TEXT, snoozed_until TEXT
);

CREATE TABLE files (
  id INTEGER PRIMARY KEY, created_at TEXT NOT NULL,
  name TEXT NOT NULL, mime TEXT, size INTEGER,
  sha256 TEXT UNIQUE,                   -- of plaintext, for dedupe
  r2_key TEXT NOT NULL,                 -- random storage object key, not the hash; the column name is historical, the key is used by both the local and R2 backends
  encrypted INTEGER DEFAULT 1,
  caption TEXT DEFAULT '', tags TEXT DEFAULT '',
  index_status TEXT DEFAULT 'pending',  -- pending|running|done|failed|skipped
  pages INTEGER, ocr_pages INTEGER
);

CREATE TABLE file_pages (
  file_id INTEGER, page INTEGER, source TEXT,  -- text|ocr|ocr+text
  body TEXT, PRIMARY KEY (file_id, page)
);

-- one index across notes and file pages
CREATE VIRTUAL TABLE search_fts USING fts5(
  title, tags, body,
  kind UNINDEXED, ref_id UNINDEXED, page UNINDEXED,
  tokenize = 'unicode61 remove_diacritics 2'
);
CREATE VIRTUAL TABLE search_tri USING fts5(
  title, tags, body,
  kind UNINDEXED, ref_id UNINDEXED, page UNINDEXED,
  tokenize = 'trigram'
);

CREATE TABLE jobs (
  id INTEGER PRIMARY KEY, type TEXT NOT NULL,   -- index_file|backup|...
  payload TEXT, status TEXT DEFAULT 'pending',
  attempts INTEGER DEFAULT 0, error TEXT,
  created_at TEXT, started_at TEXT, finished_at TEXT
);

CREATE TABLE llm_usage (
  day TEXT, prompt_tokens INTEGER, completion_tokens INTEGER, calls INTEGER,
  PRIMARY KEY (day)
);
```

Search ranking example: `ORDER BY bm25(search_fts, 8.0, 5.0, 1.0)` (title, tags, body).

Text normalization before indexing and before querying: Unicode NFC, lowercase, collapse whitespace, re-join words split by hyphen at line ends ("depo-\nsit" becomes "deposit"), drop junk-only OCR lines.

---

## 9. Security

- Responses only to your Telegram user ID.
- All secrets live in `/etc/secondbrain/env` (mode 600, owned by the service user). Never commit secrets. Add `.env*` to `.gitignore`.
- The service runs as the `secondbrain` user with systemd hardening (`NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `ReadWritePaths` limited to the data directories).
- The LLM has **no** shell, filesystem or arbitrary-HTTP tool. Only the tools listed in Section 4.2.
- **Prompt injection:** web search results are untrusted. Wrap them in clear delimiters, tell the model to ignore instructions inside them, and block write or delete tools in the same turn after a web search unless you confirm with a button.
- Telegram local API server and the bot bind to `127.0.0.1` only.
- ntfy is set to `auth-default-access: deny-all`, with a token for the bot and your user for the phone.
- Do not log message contents at info level. Log metadata only.
- Back up your **age key** separately and offline (password manager). If you lose it, every encrypted file in the active storage backend is unrecoverable. If the VM is compromised, the key on it is exposed, so keep the VM patched (`unattended-upgrades`).

---

## 10. Key flows

### 10.1 What happens when you upload a file

1. You send a PDF as a File. The local Bot API server receives it and, in local mode, the file is already on the VM's disk.
2. Bot checks your user ID, logs to `inbox`, streams the file to compute SHA-256, checks for a duplicate.
3. Streams it through `age` encryption into the active storage backend (local: a file under the objects directory; R2 only if that backend is configured: a multipart upload). The key is a random ID. Inserts a `files` row.
4. Replies immediately: "Saved lease.pdf (40 MB). Indexing in background." You can keep using the bot.
5. Inserts an `index_file` job. A single worker picks it up.
6. For each page: `pdftotext`. If the page text is under about 30 characters, rasterize and OCR. If the page contains sizable images and `OCR_IMAGE_PAGES=true`, OCR it too and merge. Write `file_pages`, update both FTS tables, checkpoint progress.
7. Progress message edits itself: "Indexing 40/300 pages".
8. Clean up temp images and the cached copy in the local Bot API directory.
9. Final message: "Indexed lease.pdf: 24 pages, 6 OCR'd." If you sent a voice note within 90 seconds, its transcript is stored as the caption and tags.

### 10.2 What happens when you search

1. You type or say "search file which has document and deposit".
2. Voice is transcribed first and shown back to you.
3. Trigger regex matches, filler words are dropped (`file which has and the ...`), keywords become `document deposit`.
4. Query runs AND, then OR, then trigram fallback. The bot says which mode it used.
5. Results list: file name, page, highlighted snippet, [Send file].
6. Tap Send file. The bot downloads from the active storage backend, decrypts, sends it, deletes the temp copy.

### 10.3 What happens with a critical reminder

1. Scheduler finds a due reminder with `critical=1`.
2. Sends a Telegram message with Done and Snooze buttons, and a POST to your ntfy topic with `Priority: urgent`.
3. Sets `next_attempt_at = now + 5 min`.
4. If you have not tapped Done by then, it repeats. After the maximum attempts it stops and writes a note in `/status`.
5. Done marks it acknowledged and, for recurring reminders, computes the next occurrence.

---

## 11. Configuration reference (`/etc/secondbrain/env`)

```ini
# Telegram
TELEGRAM_BOT_TOKEN=
TELEGRAM_ALLOWED_USER_ID=
TELEGRAM_API_BASE=http://127.0.0.1:8081      # local Bot API server
TELEGRAM_FILE_CACHE_DIR=/var/lib/telegram-bot-api

# LLM (OpenAI-compatible chat completions with tool calling)
LLM_BASE_URL=https://generativelanguage.googleapis.com/v1beta/openai/
LLM_API_KEY=                    # Gemini API key
LLM_MODEL=gemini-3.5-flash-lite
LLM_FALLBACK_MODELS=gemini-3.1-flash-lite,groq:openai/gpt-oss-20b
LLM_DAILY_TOKEN_BUDGET=100000
LLM_SEARCH_INTENT=false          # true lets the LLM extract keywords for searches

# Groq: used for the final chat fallback and voice transcription
GROQ_API_KEY=
STT_MODEL=whisper-large-v3
STT_LANGUAGE=auto                # en | hi | mr | auto

# Storage: local is active on the deployed VM; R2 is an optional alternative backend
FILE_STORAGE_BACKEND=local
LOCAL_STORAGE_DIR=/var/lib/secondbrain/data/objects
# Only when FILE_STORAGE_BACKEND=r2 (Cloudflare R2, S3 API):
# R2_ACCOUNT_ID=
# R2_ACCESS_KEY_ID=
# R2_SECRET_ACCESS_KEY=
# R2_BUCKET=
# R2_ENDPOINT=https://<ACCOUNT_ID>.r2.cloudflarestorage.com
AGE_RECIPIENT=                   # public key
AGE_IDENTITY_FILE=/etc/secondbrain/age.key

# Web search
FIRECRAWL_API_KEY=
FIRECRAWL_DAILY_LIMIT=25

# ntfy
NTFY_URL=https://ntfy.example.com
NTFY_TOPIC=secondbrain-alerts
NTFY_TOKEN=

# OCR
OCR_LANGS=eng                    # e.g. eng+hin+mar (slower, less accurate when mixed)
OCR_DPI=300
OCR_IMAGE_PAGES=true
OCR_MIN_TEXT_CHARS=30

# App
TZ=Asia/Kolkata
DATA_DIR=/var/lib/secondbrain/data
TMP_DIR=/var/lib/secondbrain/tmp
GOMEMLIMIT=200MiB
```

---

## 12. Coding-agent prompts: setup

### Prompt 0 — Master context (paste this first, or save it as `AGENTS.md` / `CLAUDE.md`)

```text
You are helping me build "Second Brain Bot", a private Telegram assistant in Go,
running on an Ubuntu VM with 2 vCPU, 1 GB RAM and 30 GB disk.

The full design is in docs/DESIGN.md. Read it completely before writing code.
It is the source of truth for architecture, data model, privacy rules and phases.

Hard rules:
1. Privacy: no AI/LLM may ever see the contents of my files, photos, documents,
   OCR text, notes search results or search results of any kind. Search runs
   locally. "Direct-reply" tools send results to Telegram, never back to the model.
2. No embeddings or vector search. Keyword search only (SQLite FTS5 + trigram).
3. Memory is tight (1 GB). One heavy job at a time. Stream files, never load
   them fully into memory. Respect GOMEMLIMIT and OMP_THREAD_LIMIT=1.
4. Never lose input: log every incoming message to the inbox table first.
5. Only my Telegram user ID gets responses.
6. Secrets only in environment variables loaded from /etc/secondbrain/env.
   Never hardcode, never commit, never print them in logs.
7. Do not invent API details. When you use Telegram, Groq, local storage or optional Cloudflare R2, ntfy,
   Firecrawl, age or any library, check the current official documentation first
   and tell me which page you used.
8. Work one phase at a time. At the end of each phase: run the tests, show me
   how to verify it myself, list what you changed, then STOP and wait for me.
9. If something in the design is ambiguous or you want to change it, ask me
   before deciding.
10. Prefer simple, boring code with tests for the tricky parts (time parsing,
    recurrence, search queries, job resume, encryption round-trip).

First task: read docs/DESIGN.md, then reply with (a) a short summary of the
system in your own words, (b) any questions or concerns about the design,
and (c) the project layout you propose. Do not write code yet.
```

---

## 13. Coding-agent prompts: build phases

After each phase: run it, test it from Telegram, commit to git, then move on.

### Phase 0 — Repo and VM prep

```text
Phase 0. Create the Go module and project layout (cmd/secondbrain, internal/
packages for config, db, telegram, router, notes, reminders, files, ocr,
search, llm, stt, ntfy, firecrawl, storage). Add Makefile targets for build,
test and a cross-compiled linux build, a .gitignore (including .env*), and a
config loader that reads the env vars listed in DESIGN.md section 11 and fails
fast with a clear message if a required one is missing for an enabled feature.

Then give me, as a numbered list, the exact commands to run on my VM for
section 7.1 of DESIGN.md (swap, user, directories, packages, firewall), and
explain what each one does in one line. Do not run anything on the VM yourself
unless I confirm.
```

### Phase 1 — Core bot skeleton

```text
Phase 1. Implement: structured logging (no message contents at info level),
SQLite open + migrations runner (create the schema from DESIGN.md section 8),
Telegram long polling through the configurable API base URL, the allowlist
check, the inbox logging step, and commands /start, /help and /status
(RAM, swap, disk, uptime, queue length). Add a systemd unit file with the
hardening options from section 9 and MemoryHigh/MemoryMax placeholders.
Add tests for the allowlist and migrations.

Show me how to run it locally with a test bot token and how to deploy it on
the VM as a service.
```

### Phase 2 — Notes and local search

```text
Phase 2. Implement notes: /note <text> and "plain text becomes a note" as the
router fallback. Implement the unified search index (search_fts and
search_tri), text normalization, and /find <query> with this strategy:
AND first, then OR, then trigram fallback, with BM25 weights
(title 8, tags 5, body 1), snippet highlighting, pagination and
filters type:, after:, before:. The search must never call any network service.
Add tests for normalization, query building and ranking with sample data
including a deliberately misspelled word.
```

### Phase 3 — Reminders and tasks (no LLM yet)

```text
Phase 3. Implement reminders and tasks with deterministic commands first:
/remind <when> | <text>, /remind critical ..., /reminders, /tasks, /done <id>,
/cancel <id>. Parse times in my configured timezone (support "in 2h", "tomorrow
18:00", ISO dates, and weekday names). Store UTC. Implement a scheduler loop
(every 20 s) that is restart-safe, fires overdue reminders with "(late)",
and supports Done / Snooze 10m / Snooze 1h inline buttons. Implement
recurrence (daily, weekly, monthly, cron) using robfig/cron. Write thorough
tests for time parsing, DST-free IST handling, recurrence and restart behavior.
```

### Phase 4 — LLM brain (tool calling)

```text
Phase 4. Add the LLM brain behind an interface, using an OpenAI-compatible
chat completions client with tool calling configured by LLM_BASE_URL,
LLM_API_KEY and LLM_MODEL. Try LLM_FALLBACK_MODELS in order if a provider
request fails; use Gemini 3.5 Flash-Lite, Gemini 3.1 Flash-Lite, then Groq
GPT-OSS 20B by default. Preserve Gemini 3 thought signatures in tool-call
history. System prompt must include the current date, time and timezone.
Tools: add_note, search_notes, set_reminder, list_reminders,
cancel_reminder, add_task, list_tasks, complete_task. Rules:
- search_notes is a DIRECT-REPLY tool: run the local search and send results
  straight to Telegram; do not return result text to the model.
- Destructive tools require a confirmation button.
- Router fallback: if the LLM errors, times out, or the daily token budget
  (LLM_DAILY_TOKEN_BUDGET) is exceeded, save the message as a note and tell me.
- Log token usage to llm_usage and add /usage.
- Messages starting with search/find stay local and never reach the LLM,
  unless LLM_SEARCH_INTENT=true.
Add tests with a fake LLM server.
```

### Phase 5 — Voice notes

```text
Phase 5. Implement voice notes: download the Telegram voice file, send it to
Groq's OpenAI-compatible transcription endpoint with STT_MODEL defaulting to
whisper-large-v3 (not the turbo model) and
STT_LANGUAGE, reply with the transcript ("Heard: ...") plus Edit and Undo
inline buttons, then route the transcript through the normal router (local
search trigger first, then the LLM brain). If Groq rejects the OGG, convert
with ffmpeg to 16 kHz mono FLAC and retry. Handle rate limits with backoff
and a clear message to me. Check Groq's current docs for the endpoint, limits
and supported formats and tell me what you found.
```

### Phase 6 — Local Bot API server, file receive, encryption, storage (local active, R2 optional)

```text
Phase 6. Implement file storage:
1. Give me step-by-step instructions to run the Telegram local Bot API server
   (Docker image or binary), including the logOut call needed before switching
   a bot from the cloud API, binding to 127.0.0.1, the volume path mapping so
   getFile paths resolve on the host, and cleanup of cached files.
2. Handle document, photo and video messages. Stream, never buffer whole files.
3. Compute SHA-256 and dedupe against files.sha256.
4. Encrypt with age (recipient public key from AGE_RECIPIENT) in a streaming
   pipe into the active storage backend (local file write; R2 multipart upload
   with aws-sdk-go-v2 only when FILE_STORAGE_BACKEND=r2). The storage key is a random ID.
5. Insert the files row, reply "Saved ... Indexing in background", enqueue an
   index_file job, delete temp files and the Bot API server's cached copy.
6. Caption text becomes caption and tags. A voice note within 90 seconds of a
   file attaches its transcript to that file.
7. Add a round-trip test: encrypt -> upload -> download -> decrypt -> compare hash.
If the R2 backend is used, also add an R2 lifecycle rule suggestion to abort incomplete multipart uploads.
```

### Phase 7 — Indexing worker (OCR)

```text
Phase 7. Implement the background job worker (single worker, jobs table,
resets 'running' jobs to 'pending' on startup) and the index_file job:
- PDFs: pdfinfo for page count; per page pdftotext; if text shorter than
  OCR_MIN_TEXT_CHARS, rasterize that one page with pdftoppm at OCR_DPI,
  optionally clean with ImageMagick (grayscale, deskew), run tesseract with
  OCR_LANGS and OMP_THREAD_LIMIT=1.
- If OCR_IMAGE_PAGES=true, use pdfimages -list to find pages with significant
  images and OCR those too, merging with the text layer.
- Images: tesseract directly. Videos: mark index_status=skipped.
- Checkpoint per page in file_pages so a crash resumes. Update both FTS tables.
- Normalize and de-hyphenate text before indexing.
- Progress message that edits itself; final summary message.
- Run subprocesses with timeouts and memory limits; one page at a time;
  delete temp files after each page.
Test with: a text PDF, a scanned PDF, a text PDF containing an embedded
screenshot with words, and a photo of a printed page. Report OCR speed and
peak memory on the VM.
```

### Phase 8 — File search and retrieval UX

```text
Phase 8. Extend /find and the voice/text "search ..." trigger to cover file
pages. Results show file name, page number, highlighted snippet and a
[Send file] button. Add filters (type:pdf|photo|video, after:, before:, tag:).
Implement Send file: stream from the active storage backend, decrypt, send with the right Telegram
method (sendDocument/sendPhoto/sendVideo; use the local server for big
uploads), then delete the temp copy. Add /files (recent list), /tag <id> <tags>
and /delete <id> (with confirmation, removes the storage object, rows and index).
Everything stays local; nothing goes to an LLM.
```

### Phase 9 — ntfy and critical alarms

```text
Phase 9. Implement critical reminders: on due, send the Telegram message and a
POST to NTFY_URL/NTFY_TOPIC with Authorization: Bearer NTFY_TOKEN, header
Priority: urgent, a title and the reminder text. Repeat every 5 minutes up to
12 attempts until I tap Done. Persist attempts in the DB so restarts do not
lose state. Add a /testalarm command. Then walk me through, step by step,
installing and configuring self-hosted ntfy and exposing it with Cloudflare
Tunnel (see my "Explain it to me" prompts); wait for me to confirm each stage.
```

### Phase 10 — Web search with Firecrawl

```text
Phase 10. Add the web_search tool using Firecrawl's search endpoint
(POST https://api.firecrawl.dev/v2/search, Authorization: Bearer
FIRECRAWL_API_KEY, small limit, no full-page scraping by default). Check the
current docs for request and response shape first. Return only title, URL
and a short snippet to the LLM, wrapped in clear untrusted-content delimiters.
In the same turn after a web search, block all write and delete tools unless I
confirm with a button. Add timeouts, error handling and a daily call cap.
```

### Phase 11 — Hardening, backups, monitoring

```text
Phase 11. Add: nightly SQLite backup (use the SQLite backup mechanism, not a
file copy), encrypt with age and save to the active storage backend under backups/ with 14-day
retention, via a systemd timer; a /backup command; disk and failed-job alerts
sent through ntfy at normal priority; log rotation; final systemd hardening;
a restore procedure written as a numbered runbook in docs/RUNBOOK.md, tested
once end to end on a scratch directory. Also write docs/OPERATIONS.md with how
to rotate each API key, update the binary, and recover from a lost VM
(including where my age key backup must be).
```

### Phase 12 — Acceptance run

```text
Phase 12. Run the acceptance checklist in DESIGN.md section 15 against the real
VM with me, one item at a time. For each item tell me exactly what to send to
the bot and what I should see. Record pass/fail and measured RAM, swap and disk
numbers. Fix failures, then summarize remaining limitations honestly.
```

---

## 14. "Explain it to me" prompts (accounts, keys, servers)

Paste these into your coding agent when you reach the phase that needs them. Each one tells the agent to guide you step by step and wait for you.

Common instruction included in each: *never ask me to paste a secret into this chat; tell me to put it directly into `/etc/secondbrain/env` on the VM.*

### A1 — Telegram bot token and your user ID

```text
Explain to me, step by step, how to create a Telegram bot with BotFather and
get its token, how to find my own numeric Telegram user ID, how to set the
bot's command menu with /setcommands (give me the exact list for our commands),
and how to turn off group privacy and disable the bot being added to groups.
Wait for me to confirm each step. Tell me to put the token and my user ID into
/etc/secondbrain/env on the VM; do not ask me to paste secrets into the chat.
Check Telegram's current docs and tell me what you used.
```

### A2 — Telegram api_id and api_hash (needed by the local Bot API server)

```text
Explain how to get a Telegram api_id and api_hash from my.telegram.org for
running the local Bot API server, what they are for, and how to store them
safely. Then explain the logOut requirement when moving a bot from the
cloud Bot API to a local server (including the waiting period), and give
me the exact commands, one at a time, waiting for me after each.
```

### A3 — Groq API key

```text
Explain how to create a Groq account, get an API key, and test transcription
with a curl command and a short sample voice file, using the model
whisper-large-v3. Tell me the current free-tier limits and maximum file size
according to Groq's docs, and what happens when I hit a limit.
```

### A4 — LLM provider key

```text
I need an LLM provider for the bot's chat brain (short messages, tool calling,
low volume). Compare 3-4 options that offer an OpenAI-compatible endpoint
with tool calling (for example Groq-hosted chat models, Google AI Studio's
Gemini, OpenRouter, and Anthropic), including current free tiers, price per
million tokens, and data-use or training terms on free tiers. Ask me which I
prefer, then explain step by step how to get the key and what to set for
LLM_BASE_URL, LLM_API_KEY and LLM_MODEL. Include a curl test that exercises
tool calling.
```

### A5 — Cloudflare R2 bucket and keys (optional backend only — skip this if you use local storage)

```text
Explain step by step how to enable Cloudflare R2, create a private bucket,
create an API token scoped to that bucket with Object Read and Write, find
my account ID and S3 endpoint, and fill in the R2_* variables. Tell me whether
R2 currently requires a payment method and what the free-tier limits are
(storage, operations, egress) from Cloudflare's current pricing page. Show me
how to add a lifecycle rule to abort incomplete multipart uploads. Give me an
aws-cli or rclone test command to verify the credentials.
```

### A6 — age encryption key

```text
Explain what age is and how my file encryption will work. Give me the commands
to generate a key pair (age-keygen) on the VM with the identity file at
/etc/secondbrain/age.key (mode 600, owned by the service user), where to find
the public recipient for AGE_RECIPIENT, and exactly how I should back up the
private key offline. Make me confirm the backup before we continue, and
explain clearly what happens if I lose it.
```

### A7 — Firecrawl API key

```text
Explain how to create a Firecrawl account and get an API key (it starts with
fc-), what the free allowance is according to their current pricing page, how
credits are consumed by the search endpoint, and give me a curl test against
POST https://api.firecrawl.dev/v2/search with Authorization: Bearer. Explain
how I can see my usage and how to cap it.
```

### A8 — Self-hosted ntfy

```text
Walk me through installing a self-hosted ntfy server on my Ubuntu VM using the
official apt repository, with a /etc/ntfy/server.yml that sets base-url,
listen-http on 127.0.0.1, auth-file, auth-default-access: deny-all,
behind-proxy: true, a cache file, and upstream-base-url (needed for instant
iOS notifications). Then: create my admin user, create an access token for the
bot, restrict the topic with ntfy access, test with a curl publish using
Priority: urgent. Then explain the phone side: installing the ntfy app
(ask me whether I use Android or iPhone first), subscribing to my topic with
login, and how to make urgent notifications bypass Do Not Disturb or Focus on
my platform, with honest limits. Check ntfy's current docs and tell me what
you used. Wait for me after each stage.
```

### A9 — Cloudflare Tunnel for ntfy

```text
Explain how to expose ntfy at https://ntfy.<my-domain> through a Cloudflare
Tunnel with no open inbound ports. Use cloudflared on the VM, show both the
dashboard (Zero Trust) route and the CLI route, let me choose, then give the
commands one at a time to install, authenticate, create the tunnel, route DNS
for the hostname, point the ingress at http://127.0.0.1:2586, and install it as
a systemd service. Show how to verify it, and confirm that SSE/websocket
connections from the ntfy app work through the tunnel.
```

### A10 — Local Telegram Bot API server

```text
Explain how to run the Telegram local Bot API server on my small VM. Compare
the Docker image (aiogram/telegram-bot-api) with a prebuilt binary, including
RAM overhead of Docker. Warn me that building from source needs a lot of RAM.
Give me the exact docker run or systemd commands with --local mode, 127.0.0.1
binding, TELEGRAM_API_ID and TELEGRAM_API_HASH from env, and a volume mapping
so file paths returned by getFile resolve correctly on the host. Explain how
files get cleaned up. Verify details in the project's current README.
```

---

## 15. Acceptance checklist

| # | Test | Expected |
|---|---|---|
| 1 | Message from a different Telegram account | No reply |
| 2 | `/status` | Shows RAM, swap, disk, queue, uptime |
| 3 | Send plain text "buy milk" | Saved as note |
| 4 | `/find milk` | Finds the note |
| 5 | "remind me tomorrow 6pm to call bank" (typed) | Reminder created with correct IST time, echoed back |
| 6 | Same sentence as a voice note | Transcript shown, reminder created, Undo works |
| 7 | Reminder due now | Message arrives with Done and Snooze buttons |
| 8 | Reboot the VM with a pending reminder | Fires (marked late) after boot |
| 9 | Critical reminder, ignore it | Phone alarm repeats every 5 min; Done stops it |
| 10 | Send a 150 MB video as File | Saved, no timeout, RAM stays safe |
| 11 | Send a text PDF | Indexed, search finds a word inside it with page number |
| 12 | Send a scanned PDF | OCR runs in background, search finds a scanned word |
| 13 | PDF with a screenshot containing a unique word | Search finds that word |
| 14 | Search with a one-letter typo | Trigram fallback still finds it |
| 15 | Say "search file which has document and deposit" | Local search, correct files, no LLM call in logs |
| 16 | Tap Send file | File arrives identical (verify hash) |
| 17 | Look at the active storage (local objects directory on the VM, or the R2 bucket if that backend is configured) | Objects are unreadable ciphertext |
| 18 | Upload the same file twice | Deduplicated |
| 19 | Kill the service mid-indexing | Resumes from the last page after restart |
| 20 | Ask "what's the latest on <topic>" | Web search answer via Firecrawl |
| 21 | Hit the LLM daily budget | Messages are saved as notes with a notice |
| 22 | Run backup and a restore into a scratch dir | Restored DB opens and searches |
| 23 | Peak RAM during a large scanned PDF | Stays below limits, no OOM kills |

---

## 16. Known limits and honest trade-offs

- Keyword search only finds the words the document contains. "Contract" will not find a page that only says "agreement" unless you tag it or add synonyms.
- Photos without text (a beach, a whiteboard sketch) are searchable only by your caption, tags and date.
- Tesseract is weaker on handwriting, blurry photos and complex tables. Printed text scans work well. Preprocessing matters more than any search trick.
- OCR on a 300-page scan can take a long time on 2 vCPUs. It runs in the background and reports progress.
- Free tiers (Groq, Firecrawl, LLM providers, R2 if used) change. Re-check limits and data-use terms before relying on them.
- Telegram sees everything you send the bot. Encryption protects files at rest in the storage backend, not inside Telegram.
- Losing the age key means losing access to all stored files.
- Voice transcription of mixed-language speech needs testing on your own voice.

## 17. Settled decision and open questions

The LLM provider order is Gemini 3.5 Flash-Lite, Gemini 3.1 Flash-Lite, then
Groq GPT-OSS 20B. `GROQ_API_KEY` also powers Groq Whisper Large v3 transcription.
Other open questions:

1. **Android or iPhone?** This changes how ntfy alarms behave.
2. **Keep client-side encryption on by default?** It is on in this design. The cost is that files can only be viewed through the bot.
3. **Keep the original voice-note audio?** Default is to delete it after transcription and keep only the transcript.
4. **Go or Python?** The doc assumes Go.
5. **Search phrasing:** fixed "search/find" trigger with no LLM (default), or let the LLM extract keywords from any sentence (`LLM_SEARCH_INTENT=true`)?
