# Acceptance run record

The acceptance checklist is designed for the real VM and Telegram account. It cannot be marked passed from source inspection. Complete each row with the user, record actual RAM/swap/disk measurements from `/status` or `free -m`, and capture pass/fail plus relevant logs without secrets.

| # | Check | Expected result | Status | Measurement / notes |
|---|---|---|---|---|
| 1 | Message from a different Telegram account | No reply | Not run: live Telegram credentials/account required | |
| 2 | `/status` | RAM, swap, disk, queue, uptime | Not run: VM required | |
| 3 | Send `buy milk` | Saved as a note | Not run: live bot required | |
| 4 | `/find milk` | Finds the note | Not run: live bot required | |
| 5 | Typed reminder for tomorrow 18:00 | Correct configured-zone time echoed | Not run: live bot required | |
| 6 | Same reminder as voice note; Undo | Transcript shown; action undone | Not run: Groq key and live bot required | |
| 7 | Reminder due now | Telegram message with Done/Snooze | Not run: live bot required | |
| 8 | Reboot with pending reminder | Fires late after reboot | Not run: VM access required | |
| 9 | Ignore critical reminder | ntfy repeats every 5 minutes; Done stops it | Not run: ntfy host, phone, and live bot required | |
| 10 | Upload 150 MB video as File | Saved without timeout; memory remains safe | Not run: local Telegram Bot API and R2 required | |
| 11 | Text PDF | Search returns matching word and page | Not run: R2 and OCR packages required | |
| 12 | Scanned PDF | OCR result searchable | Not run: VM and sample document required | |
| 13 | PDF screenshot | Embedded image word searchable | Not run: VM and sample document required | |
| 14 | Search with one-letter typo | Trigram fallback finds intended result | Not run: runtime test required | |
| 15 | Voice search phrase | Local search; no LLM call | Not run: live bot required | |
| 16 | Send file button | Retrieved bytes match original SHA-256 | Not run: R2 and Telegram required | |
| 17 | Inspect R2 object | Stored bytes are age ciphertext | Not run: R2 credentials required | |
| 18 | Upload duplicate | Existing file ID reported | Not run: R2 and Telegram required | |
| 19 | Kill service during indexing | Resumes from last page | Not run: VM and sample PDF required | |
| 20 | Ask for latest news/topic | Firecrawl-backed answer with sources | Not run: LLM and Firecrawl keys required | |
| 21 | Exceed daily LLM budget | Message saved as note with notice | Not run: configured LLM and live bot required | |
| 22 | Backup and scratch restore | Restored DB integrity/search pass | Not run: live R2 credentials and scratch database required | |
| 23 | Peak RAM on large scan | Within configured memory limits, no OOM | Not run: target VM and sample scan required | |

## Build verification status

`go build ./...`, `go vet ./...`, and a Linux/amd64 cross-build passed. Tests passed for `db`, `firecrawl`, `llm`, `ntfy`, `reminders`, and `router`; Windows Application Control blocked launching the `search.test.exe` and `storage.test.exe` binaries. The restore flow and backup retention still need a live R2 bucket for end-to-end verification. Do not treat the acceptance rows as passed until recorded with measurements.
