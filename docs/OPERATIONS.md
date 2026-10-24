# Operations

## Secrets and key rotation

All service credentials live in `/etc/secondbrain/env` on the VM, mode `0600`, owned by root or the service administrator. Never send secret values in chat, commit them, or include them in support logs. After editing the file, restart with `sudo systemctl restart secondbrain` and inspect `sudo journalctl -u secondbrain -n 50 --no-pager`.

- **Telegram bot token:** create a replacement with BotFather, update `TELEGRAM_BOT_TOKEN`, restart, test `/status`, then revoke the old token.
- **Gemini key:** create a replacement key, update `LLM_API_KEY`, restart, send a low-cost chat request, and revoke the old key. Keep the Gemini OpenAI-compatible base URL and model ID aligned.
- **Groq key:** `GROQ_API_KEY` is used for the Groq GPT-OSS 120B chat fallback and Whisper Large v3 transcription. Replace it, restart, verify a chat fallback or short voice note, then revoke the old key.
- **Firecrawl key:** replace `FIRECRAWL_API_KEY`, restart, test a web query, and revoke the old key. `FIRECRAWL_DAILY_LIMIT` limits local calls per UTC day.
- **Storage credentials:** for R2, create a new bucket-scoped read/write token, update `R2_ACCESS_KEY_ID` and `R2_SECRET_ACCESS_KEY`, restart, verify file retrieval and `/backup`, then revoke the old token. Local storage has no provider credentials, but its encrypted objects and backups reside on the VM disk; copy backups to another device.
- **ntfy token:** create a new restricted token, update `NTFY_TOKEN`, restart, send `/testalarm`, then revoke the old token.
- **age identity:** do not casually rotate it. Existing objects are encrypted to the current recipient and cannot be decrypted by a new identity. To rotate, decrypt and re-encrypt every stored object and every retained backup, verify hashes/restores, then replace the identity and recipient. Preserve the old private identity offline until migration is verified.

Back up `/etc/secondbrain/age.key` offline in a password manager or encrypted removable media. The VM copy is operational; it is not a recovery backup. Losing all copies makes every encrypted file and backup unrecoverable.

## Updating the binary

The preferred release path is GitHub Actions: a push to `main` builds the Linux amd64 binary, connects to the private VM through an ephemeral Tailscale runner, and runs `/usr/local/sbin/secondbrain-deploy`. The helper retains the prior binary and rolls back if the service does not remain active. See `docs/DEPLOYMENT.md` for the one-time setup.

For a manual release from Windows, follow the exact build, upload, backup, rollback, and verification steps in the [VM operations guide](VM.md). GitHub pushes to a non-`main` branch do not deploy the bot.

Apply database schema changes through the built-in numbered migration runner. Take a backup before a release that changes the schema.

## Lost VM recovery

1. Provision a new supported Ubuntu VM, create the `secondbrain` service account/directories, install the packages from `docs/DESIGN.md` section 7.1, and copy a known-good binary.
2. Restore `/etc/secondbrain/env` from your secure password manager. Do not reuse a VM image that contains a compromised age key or API token.
3. Restore `/etc/secondbrain/age.key` from its offline backup and set ownership to `secondbrain:secondbrain`, mode `0600`. Verify its public recipient matches `AGE_RECIPIENT`.
4. Install the systemd units from `deploy/systemd`, reload systemd, and enable the backup timer. Start the service and check logs.
5. Restore the latest encrypted SQLite backup following `docs/RUNBOOK.md`; then send `/find` and retrieve one known file to verify database search and age/R2 access.
6. Rotate credentials that may have been exposed by the lost VM. If it was compromised, rotate all provider keys and invalidate the old VM before restoring.

## Monitoring and logs

Logs go to journald. Inspect with `journalctl -u secondbrain`; journald handles rotation according to the host's journal limits. The service sends a normal-priority ntfy alert when disk usage crosses 80% and when the count of failed background jobs increases. Critical reminders use urgent priority. `/status` reports runtime resource data and queue length.

## Backup schedule

`secondbrain-backup.timer` runs daily near 02:15 local system time. It creates a SQLite `VACUUM INTO` snapshot, encrypts it with age, uploads under `backups/`, and deletes recorded backups older than 14 days. `/backup` runs the same operation immediately. Verify timer state with `systemctl list-timers secondbrain-backup.timer` and inspect `journalctl -u secondbrain-backup`.
