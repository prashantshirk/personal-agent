# Second Brain deployment handoff

This file is a handoff for another coding assistant. It summarizes the project, the Azure VM, deployment history, architecture, commands, credentials handling, and current state. **It intentionally contains no tokens, API hashes, passwords, encryption private keys, R2 secrets, or complete Telegram file IDs.** Never ask the user to paste those secrets into chat.

Last updated: 2026-10-11 (Asia/Kolkata). Project workspace is the root of this repository. The user's SSH alias is configured locally; use `<configured-ssh-alias>` rather than documenting its value or the private VM address.

## Current state / next action

- Both `telegram-bot-api.service` and `secondbrain.service` were observed active after the user said the bot was working again.
- The user re-sent a 42.4 MB (44,563,135-byte), 52-page scanned PDF; its private filename and caption are omitted.
- It is saved as file ID 3 in SQLite and encrypted local object storage. The background job is actively indexing it. The document has 52 pages. At the last check, 30 page rows had been indexed; the job was `running`, with no error. `index_status` and page totals are finalized only after completion. OCR had previously been observed working on the PDF (Tesseract processing a page) and on a test photo.
- Do not ask the user to resend the PDF. Wait for the index job to finish, then check the DB safely and ask the user to search a distinctive word visible in the scanned pages with `/find <word>`. Check that the result names this PDF and includes a page number/snippet. The bot's `/files` can show the current indexing state.
- A prior user search returned “No matches found” because it was issued before the current upload/indexing. Later database records confirm the PDF was successfully received and queued.
- GitHub Actions deployment automation was discussed but intentionally deferred. The repo has deployment documentation/workflow material; one-time GitHub/Tailscale secrets and runner setup were not confirmed complete. Manual deployments continue to use the user's configured SSH alias and `scp`.
- User copied a tar of local encrypted `objects` to a Windows Documents folder and removed the temporary tar from the VM. Exact local path omitted. An offline copy of `/etc/secondbrain/age.key` was not confirmed; that private key must be backed up separately and securely for recovery.

## User / VM facts shared

| Item | Detail |
|---|---|
| Cloud / OS | Azure VM, Ubuntu 22.04.5 LTS |
| Network | No public IP. Tailscale is installed and used for administration. No inbound listener is needed for the bot; local Bot API binds only to loopback. Private VM address and local SSH alias intentionally omitted. |
| Architecture | `x86_64` / Linux amd64 |
| Memory | About 891 MiB RAM, 2 GiB swap. A later check showed roughly 458 MiB available before OCR; OCR temporarily uses more. |
| Disk | Root filesystem 62 GB, about 57 GB available at the time checked. |
| CPU | VM is small (roughly 2 vCPU per design assumptions; actual `nproc` was not recorded in the handoff). |
| Go on user's Windows PC | Go 1.27.0, windows/amd64; `C:\Program Files\Go\bin\go.exe`. Module declares Go 1.22. |
| User Telegram ID | Configured as `TELEGRAM_ALLOWED_USER_ID`; exact value omitted from this public handoff. |
| Telegram bot | Single-user bot; exact username omitted. Never record/publish its token. |
| VM database | `/var/lib/secondbrain/data/secondbrain.db` |
| App executable on VM | `/opt/secondbrain/secondbrain` |
| App runtime user | `secondbrain` system user |

## What the project does

Second Brain is a single-user Go Telegram assistant. Telegram long polling means no webhook, domain, public IP, or inbound firewall rule is needed for the bot. The bot allowlists the owner's numeric Telegram ID, logs incoming updates to SQLite, and routes text, voice, files, photos, and callbacks.

Main features implemented/documented:

- Notes and local `/find` search using SQLite FTS5 plus a trigram index for partial/OCR typos. Explicit find/search requests stay local and are not sent to the LLM.
- Tasks and one-off/recurring reminders; Done/Snooze buttons. Optional urgent ntfy notifications for critical reminders.
- Gemini/OpenAI-compatible chat with function tools. User's intended order: Gemini 3.5 Flash-Lite, Gemini 3.1 Flash-Lite, then Groq `openai/gpt-oss-120b`. Groq Whisper `whisper-large-v3` (not Turbo) is used for voice transcription.
- Encrypted file storage, deduplication, asynchronous OCR/text extraction and page-level indexing. No document contents or OCR text are sent to the LLM. PDFs use `pdftotext`, and pages with little/no text or embedded images are OCR'd with Tesseract. Photos are OCR'd. Video is stored but not OCR-indexed.
- File search returns page/snippet and a Send file button.
- Encrypted database backup via `/backup` and a systemd timer.
- Optional Firecrawl web search, ntfy, R2. User previously created an R2 key but active storage was confirmed as `FILE_STORAGE_BACKEND=local` with `/var/lib/secondbrain/data/objects`; do not assume R2 is actively used.

Relevant repo docs: `README.md`, `docs/DESIGN.md`, `docs/DEPLOYMENT.md`, `docs/OPERATIONS.md`, `docs/RUNBOOK.md`, `docs/ACCEPTANCE.md`.

## Architecture

```text
Telegram app
    │ outbound HTTPS (no public IP needed)
Telegram servers
    │
    ├── cloud Bot API for normal Bot API operations
    └── local Bot API server on the VM for large-file downloads
          127.0.0.1:8081, --local, systemd service
                    │
                    ▼
          Second Brain Go binary (systemd)
             ├── Telegram update router / allowlist / inbox
             ├── SQLite: notes, tasks, reminders, files, jobs,
             │          OCR text, FTS/trigram indexes, usage
             ├── one background file index/OCR worker
             ├── reminder scheduler
             ├── encrypted local object storage (age)
             └── outbound APIs: Gemini, Groq, optional Firecrawl/ntfy
```

The local Telegram Bot API server is a **separate program**, not a Go package and not the bot itself. Telegram's official server is written in C++ and uses TDLib (Telegram's large C++ client library). It is built once and then runs as `/usr/local/bin/telegram-bot-api`; the bot remains Go. The local server raises the Bot API file-download ceiling beyond the standard cloud Bot API's 20 MB cap. It listens on `127.0.0.1:8081`, not on the public/Tailscale interface.

Upload path: Telegram update → bot inbox → local Bot API `getFile` yields an absolute VM path → Go client copies it into the bot temp directory → SHA-256 dedup → age encryption → active storage backend → SQLite `files` row and `index_file` job → single background worker decrypts to temp and indexes page-by-page → progress message and search indexes → cleanup of temporary/local Telegram cache. Scanned PDF OCR can be CPU-intensive on this sub-1-GiB VM; swap is enabled.

## Services and important paths

- `/etc/secondbrain/env`: bot token, allowed user ID, Gemini/Groq settings, paths, storage configuration, timezone, etc. Secret file. Never print it or commit it.
- `/etc/secondbrain/telegram-bot-api.env`: `TELEGRAM_API_ID` and `TELEGRAM_API_HASH` from the user's existing Telegram application. Secret file; never print it.
- `/etc/systemd/system/secondbrain.service`: app unit; `User=secondbrain`, `EnvironmentFile=/etc/secondbrain/env`, app executable, hardening, `MemoryHigh=350M`, `MemoryMax=600M`, `ProcSubset=all` from existing override, writable data/tmp/local API cache paths.
- `/etc/systemd/system/secondbrain.service.d/override.conf`: contains `[Service]` and `ProcSubset=all`; this was added because the `/status` RAM/swap reader needed `/proc/meminfo` access. Preserve this drop-in.
- `/etc/systemd/system/secondbrain.service.d/telegram-bot-api.conf`: requires/starts after `telegram-bot-api.service`.
- `/etc/systemd/system/telegram-bot-api.service`: runs the official local Bot API binary as `secondbrain`, loopback port 8081, with protected data directory and memory limits (`MemoryHigh=300M`, `MemoryMax=350M`). It reads app ID/hash from the protected environment file, not command-line flags.
- `/var/lib/telegram-bot-api`: local server state and large-file cache; writable by `secondbrain`. **Do not list full file paths under this directory in logs/output**: the upstream server can include the bot token in a per-bot directory name.
- `/var/lib/secondbrain/data/secondbrain.db`: SQLite database.
- `/var/lib/secondbrain/data/objects`: active encrypted local file object store.
- `/var/lib/secondbrain/tmp`: temporary download/decrypt/OCR files.
- `/opt/secondbrain/secondbrain.before-local-storage`: old executable backup created during an earlier rollback attempt; current live binary is `/opt/secondbrain/secondbrain`.
- `/etc/systemd/system/secondbrain-backup.service` and `.timer`: encrypted DB backup, timer enabled. Timer scheduled daily around 02:15 local VM time with randomized delay.

## Deployment history and commands

These are commands used or closely reconstructed from the session. Paths and account names match the user's setup. Commands containing credentials are shown only as placeholders or use private VM-side environment files.

### Initial VM preparation

The user checked outbound Telegram HTTPS:

```bash
getent hosts api.telegram.org
curl -sS -o /dev/null -w 'Telegram HTTPS: %{http_code}\n' \
  --connect-timeout 10 https://api.telegram.org
```

User created the service account and directories:

```bash
sudo useradd --system --create-home --home-dir /var/lib/secondbrain \
  --shell /usr/sbin/nologin secondbrain
sudo mkdir -p /etc/secondbrain /var/lib/secondbrain/data \
  /var/lib/secondbrain/tmp /opt/secondbrain
sudo chown -R secondbrain:secondbrain /var/lib/secondbrain /opt/secondbrain
sudo chmod 750 /etc/secondbrain
```

The VM has 2 GiB swap; the user observed `vm.swappiness=20` in sysctl output. Typical creation commands (from the project setup) are:

```bash
sudo fallocate -l 2G /swapfile
sudo chmod 600 /swapfile
sudo mkswap /swapfile
sudo swapon /swapfile
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
echo 'vm.swappiness=20' | sudo tee /etc/sysctl.d/99-swap.conf
sudo sysctl --system
```

### Build/deploy the Go bot

The user's local SSH alias automatically selects the configured key; a direct `scp <user>@<private-address>:...` failed `Permission denied (publickey)` because it bypassed that config. Use `scp ... <configured-ssh-alias>:/tmp/...`.

The app was cross-built on Windows (Go is installed):

```powershell
cd <Windows-project-directory>
$env:GOOS = 'linux'
$env:GOARCH = 'amd64'
go build -o .\bin\secondbrain-linux .\cmd\secondbrain
scp .\bin\secondbrain-linux <configured-ssh-alias>:/tmp/secondbrain
```

Install on the VM after the directories/user exist:

```bash
sudo install -o root -g root -m 0755 /tmp/secondbrain /opt/secondbrain/secondbrain
rm /tmp/secondbrain
file /opt/secondbrain/secondbrain
```

The user observed an ELF 64-bit x86-64 static Go executable. Earlier copy attempts failed because the destination directory did not yet exist and later because `/tmp/secondbrain` had already been moved/removed. Check paths before repeating.

### Local Bot API: official C++ server built from source

Telegram's official repo instructs users to build the server with CMake; no official prebuilt Linux binary was found. An unofficial prebuilt binary was deliberately not used. VM did not have Docker and had under 1 GiB RAM, so compiling on the VM was abandoned. A partial upstream build tree/dependencies may remain at `under the VM user home directory` on the VM; it is not used by the running service.

Build was completed in WSL using a minimal Ubuntu 22.04 (Jammy) rootfs at `/opt/ubuntu-22.04` to match the VM's glibc generation. WSL itself was Ubuntu 26.04. The source was cloned recursively from the official upstream repo and pinned at submodule commit `bc9c263e2bfee06aaab41e82db51a103376030bc`. Build used Clang/C++ libc++ and four jobs, then resumed cached work with six jobs:

```bash
sudo apt-get update
sudo apt-get install -y debootstrap
sudo debootstrap --arch=amd64 jammy /opt/ubuntu-22.04 http://archive.ubuntu.com/ubuntu
# In the Jammy rootfs, enable jammy, jammy-updates, jammy-security and universe,
# then install: ca-certificates git cmake gperf zlib1g-dev libssl-dev clang
# libc++-dev libc++abi-dev make.
sudo chroot /opt/ubuntu-22.04 git clone --recursive \
  https://github.com/tdlib/telegram-bot-api.git /opt/telegram-bot-api
sudo chroot /opt/ubuntu-22.04 bash -lc '
  cd /opt/telegram-bot-api
  mkdir -p build && cd build
  CC=/usr/bin/clang CXX=/usr/bin/clang++ CXXFLAGS="-stdlib=libc++" \
    cmake -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX:PATH=/usr/local ..
  cmake --build . --target install -j6
'
```

The executable built successfully at `/opt/ubuntu-22.04/usr/local/bin/telegram-bot-api` in WSL and was copied to project `bin/telegram-bot-api` (about 42 MB), then transferred with `scp ... <configured-ssh-alias>:/tmp/telegram-bot-api`. It dynamically links libc++; the VM had compatible runtime libraries after C++ dependencies were installed. Verified on VM with:

```bash
ldd /tmp/telegram-bot-api | grep 'not found' || true
sudo install -o root -g root -m 0755 /tmp/telegram-bot-api /usr/local/bin/telegram-bot-api
rm /tmp/telegram-bot-api
/usr/local/bin/telegram-bot-api --help
```

The help confirmed `--local`, `--api-id`, `--api-hash`, `--http-ip-address`, `--http-port`, `--dir`; the server supports API ID/hash through `TELEGRAM_API_ID` / `TELEGRAM_API_HASH` environment variables as well.

### Local Bot API config and migration

The user entered their API ID/hash privately into:

```bash
sudo nano /etc/secondbrain/telegram-bot-api.env
```

Format (values are private):

```ini
TELEGRAM_API_ID=<numeric app id>
TELEGRAM_API_HASH=<app hash>
```

The installed local Bot API service binds to loopback:

```ini
ExecStart=/usr/local/bin/telegram-bot-api --local \
  --http-ip-address=127.0.0.1 --http-port=8081 \
  --dir=/var/lib/telegram-bot-api
```

`/etc/secondbrain/telegram-bot-api.env` is loaded by systemd; credentials are not command-line args. In an early setup, command-line flags briefly caused systemd status to display the app ID/hash; this was fixed by using the environment file. Diagnostics also accidentally emitted the local Bot API's per-bot data path (which may contain the bot token). User was told to rotate the bot token with BotFather; user later said the bot was working and a new upload was verified in the DB. Do not repeat those leaks.

The local server and bot setup commands used:

```bash
sudo install -d -o secondbrain -g secondbrain -m 0750 /var/lib/telegram-bot-api
sudo systemctl daemon-reload
sudo systemctl enable --now telegram-bot-api
sudo systemctl status telegram-bot-api --no-pager
```

Before switching off Telegram cloud Bot API, the bot was stopped and Telegram cloud `logOut` returned `{"ok":true,"result":true}`. The private token was read only from `/etc/secondbrain/env`; do not put it on the shell command line or in chat. A safe VM-side pattern is:

```bash
sudo systemctl stop secondbrain
sudo bash <<'EOF'
set -eu
set -a
. /etc/secondbrain/env
set +a
printf 'url = "https://api.telegram.org/bot%s/logOut"\n' "$TELEGRAM_BOT_TOKEN" \
  | curl -sS --config -
EOF
```

Then the bot env was configured with:

```ini
TELEGRAM_API_BASE=http://127.0.0.1:8081
TELEGRAM_FILE_CACHE_DIR=/var/lib/telegram-bot-api
```

These settings are required: the Go client detects absolute `file_path` values returned by the local Bot API, copies the file from VM disk, and the router deletes only cache files under `TELEGRAM_FILE_CACHE_DIR`.

Systemd drop-in for ordering:

```ini
# /etc/systemd/system/secondbrain.service.d/telegram-bot-api.conf
[Unit]
Requires=telegram-bot-api.service
After=telegram-bot-api.service
```

Then:

```bash
sudo systemctl daemon-reload
sudo systemctl restart telegram-bot-api secondbrain
sudo systemctl status telegram-bot-api secondbrain --no-pager
```

Local `getMe` was verified successfully using a curl config piped on stdin, not by displaying the token:

```bash
sudo bash <<'EOF'
set -eu
set -a; . /etc/secondbrain/env; set +a
printf 'url = "http://127.0.0.1:8081/bot%s/getMe"\n' "$TELEGRAM_BOT_TOKEN" \
  | curl -sS --config -
EOF
```

It returned `ok: true` for the user's bot. No inbound port/firewall/Tailscale exposure is required for local port 8081.

### OCR tools and storage

Project design uses Poppler (`pdfinfo`, `pdftotext`, `pdftoppm`, `pdfimages`) and Tesseract. The VM can execute `pdfinfo` and `tesseract` (confirmed by actual PDF/photo OCR). A single background worker indexes one page at a time and stores OCR text and page rows in SQLite. Large scanned PDFs can take minutes on this small VM; the progress message is edited as pages complete.

Active storage backend was checked without exposing secrets and was:

```ini
FILE_STORAGE_BACKEND=local
LOCAL_STORAGE_DIR=/var/lib/secondbrain/data/objects
```

Objects are age-encrypted on the VM. The `/backup` command previously succeeded and the daily backup timer was enabled. A copied tar of `objects` exists in the user's Windows Documents location, but that data copy alone is not useful without the matching offline age private identity. Do not claim this is a complete disaster-recovery setup.

### Backup service/timer commands used

Service unit files were copied into `/etc/systemd/system`, and the timer was enabled:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now secondbrain-backup.timer
sudo systemctl status secondbrain-backup.timer --no-pager
sudo systemctl start secondbrain-backup.service
sudo systemctl status secondbrain-backup.service --no-pager
sudo journalctl -u secondbrain-backup.service -n 30 --no-pager
```

Manual backup completed successfully (`status=0/SUCCESS`). The bot also returned an encrypted backup object key via `/backup`. The user exported the encrypted object directory as a tar, copied it to Windows with `scp`, then removed the temporary tar from the VM:

```powershell
ssh <configured-ssh-alias> "sudo tar --exclude='*/.secondbrain-object-*' -C /var/lib/secondbrain/data -cf /home/<vm-user>/secondbrain-objects.tar objects"
ssh <configured-ssh-alias> "sudo chown <vm-user>:<vm-user> /home/<vm-user>/secondbrain-objects.tar && sudo chmod 600 /home/<vm-user>/secondbrain-objects.tar"
scp <configured-ssh-alias>:/home/<vm-user>/secondbrain-objects.tar <Windows-documents-folder>/secondbrain-objects.tar
ssh <configured-ssh-alias> "rm /home/<vm-user>/secondbrain-objects.tar"
```

### Earlier bot / provider debugging

- Cloud API HTTPS test succeeded from the VM (Telegram endpoint returned 302 on a bare request; DNS resolved IPv6).
- Direct test against Gemini OpenAI-compatible endpoint succeeded for `gemini-3.5-flash-lite` and `gemini-3.1-flash-lite` after removing a trailing slash from the base URL before adding `/chat/completions`.
- App initially got Gemini HTTP 400 `value at top-level must be a list` for tool definitions; user later saw no Gemini provider failure in logs after the code was fixed and redeployed.
- Fallback model was changed from weaker Groq 20B to `openai/gpt-oss-120b`. Groq Whisper model is `whisper-large-v3`, not Turbo.
- A manual shell test initially failed because curl `--config -` was passed in a bad order/escaped form; keep the working `printf ... | curl -sS --config -` pattern and use VM-side envs.
- `/status` initially showed RAM/swap unavailable because service had `ProcSubset=pid`. The user installed a drop-in with `ProcSubset=all`; it fixed reporting. Keep that override.

## Useful commands for future operations

All are run in PowerShell on the user's PC unless indicated. Keep output free of secrets.

```powershell
# Service state
ssh <configured-ssh-alias> "sudo systemctl status secondbrain telegram-bot-api --no-pager"

# App logs
ssh <configured-ssh-alias> "sudo journalctl -u secondbrain -n 100 --no-pager"

# Bot API logs (do not enumerate its data paths)
ssh <configured-ssh-alias> "sudo journalctl -u telegram-bot-api -n 50 --no-pager"

# VM health
ssh <configured-ssh-alias> "free -h; df -h /; sudo ss -ltnp | grep ':8081' || true"

# Database metadata only; do not dump inbox.raw (file IDs and message contents)
ssh <configured-ssh-alias> "sudo sqlite3 /var/lib/secondbrain/data/secondbrain.db 'select id,name,size,index_status,pages,ocr_pages from files order by id desc limit 10;'"
ssh <configured-ssh-alias> "sudo sqlite3 /var/lib/secondbrain/data/secondbrain.db 'select id,type,status,attempts,error from jobs order by id desc limit 10;'"

# Search/list files from Telegram
/files
/find <distinctive word from the PDF>
/status
```

The last diagnostic snapshot had file ID 3, size 44,563,135 bytes, 52 pages, 30 indexed `file_pages`, job `running`, no job error. Recheck rather than assuming the job is still running; it should eventually become `done` with `pages=52`, and an OCR count. Then test `/find` with a distinctive printed word and verify a result with the filename and page number.

## Remaining follow-up / acceptance

1. Confirm PDF indexing reaches `done`, all 52 pages are indexed, and `/find` locates a distinctive word visible only in the scanned content. If it fails, inspect latest job `error`, `secondbrain` journal, available RAM/swap, and OCR subprocess state. Never dump raw inbox rows or Bot API directory names.
2. Ask user to verify `/files`, PDF search results, and Send file retrieval.
3. Verify the bot token was rotated after the accidental diagnostic-output exposure; never ask user to paste it. Make sure current service runs with the new token and test `/status`.
4. Back up `/etc/secondbrain/age.key` offline and securely; confirm recovery can decrypt a backup. Keep a copy of encrypted local objects off VM. Current storage is local, not active R2.
5. Consider GitHub Actions/Tailscale deployment automation later, only after user wants it. Do not imply it is already configured.
6. The acceptance list in `docs/ACCEPTANCE.md` still has many end-to-end checks marked not run. Actual large-PDF saving/OCR is in progress; do not call all project acceptance complete.
