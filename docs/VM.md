# VM operations guide

This guide is for operating the deployed Second Brain bot on its Ubuntu Azure VM
from Windows PowerShell. It covers routine access, data locations, deployment,
stopping the application, and removing it. The VM is reached through Tailscale;
it does not need a public IP or inbound Telegram webhook.

> Replace `your-vm-ssh-alias` in the examples with the SSH alias configured on
> your computer. Keep VM names, SSH aliases, IPs, bot usernames, tokens, and keys
> out of this public repository.

## VM at a glance

The last reported VM measurements were Ubuntu 22.04.5 LTS, x86-64, about
891 MiB RAM, 2 GiB swap, and a 62 GB root disk. Those values can change; check
the live machine with the commands below. The app is a Go Linux/amd64 binary,
uses SQLite, and runs as the restricted `secondbrain` system user. The VM has
no public IP; your computer reaches it over Tailscale SSH.

Telegram messages and provider API calls use outbound HTTPS. Telegram long
polling means no public webhook or inbound Telegram port is required. The local
Telegram Bot API server listens only on `127.0.0.1:8081` and handles large file
downloads. The Go application and that server are separate processes.

## Connect and inspect health

Open PowerShell and set your local SSH alias once per terminal:

```powershell
$Vm = "your-vm-ssh-alias"
ssh $Vm
```

Run these from PowerShell without opening an interactive SSH shell:

```powershell
ssh $Vm "sudo systemctl is-active secondbrain"
ssh $Vm "sudo systemctl is-active telegram-bot-api"
ssh $Vm "sudo systemctl status secondbrain --no-pager"
ssh $Vm "free -h; df -h /"
ssh $Vm "sudo systemctl show secondbrain -p MainPID -p MemoryCurrent -p MemoryPeak"
```

Expected service state is `active`. `/status` in Telegram also reports uptime,
available RAM and swap, disk use, queue counts, and LLM usage.

## VM paths and what they contain

| Path | Contents and access |
|---|---|
| `/opt/secondbrain/secondbrain` | Installed application binary; root-owned and executable. |
| `/opt/secondbrain/secondbrain.previous` | Previous binary used by the optional Actions deploy helper. Manual deployments should keep their own dated copy. |
| `/home/azureuser/secondbrain.new` | Temporary incoming binary path used by the checked-in Actions deploy helper; normally removed after deploy. |
| `/etc/secondbrain/env` | Main app settings and provider secrets. Edit with `sudo nano`; never print or commit the values. |
| `/etc/secondbrain/telegram-bot-api.env` | Telegram API ID and hash for the local Bot API server. Treat as credentials. |
| `/etc/secondbrain/age.key` | Private age identity used to decrypt local objects and backups. Keep an offline copy. |
| `/var/lib/secondbrain/data/secondbrain.db` | SQLite database: inbox metadata, notes, tasks, reminders, schedules, file metadata, OCR text, search indexes, and usage counters. |
| `/var/lib/secondbrain/data/objects/` | Local age-encrypted uploaded files and encrypted database backups. This is the active storage backend on the VM. |
| `/var/lib/secondbrain/tmp/` | Temporary downloads, decrypted working files, and OCR/conversion scratch files. |
| `/var/lib/telegram-bot-api/` | Local Bot API cache for large Telegram files. Do not list its child paths: a path may contain the bot token. |
| `/etc/systemd/system/secondbrain.service` | Main bot unit. |
| `/etc/systemd/system/secondbrain.service.d/` | Local drop-ins, including `ProcSubset=all` for `/status` and Bot API ordering. Preserve these during maintenance. |
| `/etc/systemd/system/telegram-bot-api.service` | Local Telegram Bot API unit. |
| `/etc/systemd/system/secondbrain-backup.service` and `.timer` | One-shot encrypted backup service and its nightly timer. |
| `/usr/local/sbin/secondbrain-deploy` | Optional fixed deployment helper for GitHub Actions, if installed. |

The Go source is kept in the project checkout on your Windows computer and in
GitHub. The VM normally stores only the compiled Linux binary, configuration,
and runtime data; you do not need to copy the source tree to the VM.

Secrets are in files under `/etc/secondbrain`, not in the binary or repository.
To see only configured variable names (not values):

```bash
sudo cut -d= -f1 /etc/secondbrain/env
```

Edit a setting safely and restart the app:

```bash
sudo nano /etc/secondbrain/env
sudo chown root:root /etc/secondbrain/env
sudo chmod 600 /etc/secondbrain/env
sudo systemctl restart secondbrain
sudo systemctl is-active secondbrain
```

For Bot API credentials, edit `/etc/secondbrain/telegram-bot-api.env`, retain
root ownership and restrictive permissions, then restart `telegram-bot-api`
and `secondbrain`.

## Read data and logs

Prefer the bot for normal use: `/find` searches notes and indexed file text,
`/files` browses saved files, `/backup` creates an encrypted database backup,
and `/schedules` lists scheduled actions. File retrieval buttons send the
decrypted original through Telegram without exposing the storage object path.

For read-only diagnostics, use SQLite queries. Do not dump `inbox.raw`; it
contains raw Telegram update payloads. Examples:

```bash
DB=/var/lib/secondbrain/data/secondbrain.db
sudo sqlite3 -header -column "$DB" \
  "SELECT id,name,size,index_status,pages,ocr_pages FROM files ORDER BY id DESC LIMIT 20;"
sudo sqlite3 -header -column "$DB" \
  "SELECT id,type,status,attempts,error FROM jobs ORDER BY id DESC LIMIT 20;"
sudo sqlite3 -header -column "$DB" \
  "SELECT id,substr(body,1,80) AS body,created_at FROM notes ORDER BY id DESC LIMIT 20;"
sudo sqlite3 -header -column "$DB" \
  "SELECT id,kind,prompt,next_at,repeat_rule,status FROM schedules ORDER BY next_at LIMIT 20;"
sudo sqlite3 -header -column "$DB" \
  "SELECT day,prompt_tokens,completion_tokens,calls FROM llm_usage ORDER BY day DESC LIMIT 10;"
```

SQLite runs in WAL mode. Do not copy only `secondbrain.db` while the service is
running; use `/backup` or `secondbrain-backup.service` for a consistent backup.
The scheduled backup is encrypted with age and stored in the configured object
backend. With the current local backend, its object is under `objects/backups/`.
The age private key is required to recover it.

View logs without exposing environment values:

```bash
sudo journalctl -u secondbrain -n 100 --no-pager
sudo journalctl -u telegram-bot-api -n 50 --no-pager
sudo journalctl -u secondbrain-backup.service -n 50 --no-pager
sudo systemctl list-timers secondbrain-backup.timer
sudo ss -ltnp | grep ':8081' || true
```

The `:8081` listener should be `127.0.0.1:8081`, never a public interface.

## Deploy new code manually from Windows

Pushing a branch named `test` to GitHub does **not** deploy it. The checked-in
Actions workflow runs for pushes to `main`, or when manually started from the
GitHub Actions page, and only after its Tailscale/SSH repository secrets and
VM-side deploy helper have been configured. Manual deployment works without
Actions and uses your existing SSH alias over Tailscale.

1. In PowerShell, go to the project root and set the VM alias:

   ```powershell
   Set-Location -LiteralPath "path\to\the\project"
   $Vm = "your-vm-ssh-alias"
   ```

2. If this release changes the database schema, create and verify an encrypted
   backup first. Do not deploy the migration if the backup command fails:

   ```powershell
   ssh $Vm "sudo systemctl start secondbrain-backup.service; sudo systemctl show secondbrain-backup.service -p Result --value"
   ```

   The result should be `success`. The bot's `/backup` command can also create
   an on-demand backup.

3. Cross-compile for the VM. The current VM is x86-64, so use `amd64`:

   ```powershell
   New-Item -ItemType Directory -Force .\bin | Out-Null
   $env:GOOS = "linux"
   $env:GOARCH = "amd64"
   $env:CGO_ENABLED = "0"
   go build -trimpath -o .\bin\secondbrain-linux .\cmd\secondbrain
   if ($LASTEXITCODE -ne 0) { throw "Linux build failed" }
   ```

4. Upload the new binary to a temporary path:

   ```powershell
   scp .\bin\secondbrain-linux "${Vm}:/tmp/secondbrain-new"
   ```

5. Install it while preserving a uniquely named rollback copy, then restart:

   ```powershell
   ssh $Vm 'set -e; stamp=$(date -u +%Y%m%dT%H%M%SZ); sudo cp -a /opt/secondbrain/secondbrain "/opt/secondbrain/secondbrain.$stamp"; sudo install -o root -g root -m 0755 /tmp/secondbrain-new /opt/secondbrain/secondbrain; rm /tmp/secondbrain-new; sudo systemctl restart secondbrain; sleep 2; sudo systemctl is-active secondbrain; echo "Rollback binary: /opt/secondbrain/secondbrain."$stamp'
   ```

   The output should include `active`. Startup applies any pending numbered
   SQLite migrations. Confirm the service log is clean and test `/status`,
   `/find`, and the feature changed by this release in Telegram.

If startup fails, install the rollback copy path printed above, then restart:

```powershell
ssh $Vm "sudo install -o root -g root -m 0755 /opt/secondbrain/secondbrain.<timestamp> /opt/secondbrain/secondbrain && sudo systemctl restart secondbrain"
```

Use the exact timestamped path printed by the deploy command. A code rollback
does not roll back the database. The app migrations are additive; do not
replace the live database with a backup unless you intend to lose changes made
after that backup.

## Deploy automatically with GitHub Actions

The workflow in `.github/workflows/deploy.yml` builds a Linux/amd64 binary and
connects to the private VM through a temporary Tailscale runner. It runs only
on `main` or a manual `workflow_dispatch`. It does not deploy the `test` branch.
The one-time setup is documented in [Deployment and VM preparation](DEPLOYMENT.md):
install `/usr/local/sbin/secondbrain-deploy`, configure the restricted sudo
rule, create a dedicated SSH key, configure Tailscale CI access, and add the
five required GitHub Actions secrets. Provider API keys and app secrets stay
on the VM. After setup, merge/push the intended release to `main` or start the
workflow manually from GitHub → Actions.

The deploy helper retains the prior executable and attempts rollback if the
service fails to remain active. Keep manual deployments available as a fallback.

## Stop and start the bot

Stop only the app while preserving data and configuration:

```bash
sudo systemctl stop secondbrain
sudo systemctl is-active secondbrain
```

Start it again:

```bash
sudo systemctl start secondbrain
sudo systemctl is-active secondbrain
```

To keep the app from starting after a VM reboot:

```bash
sudo systemctl disable --now secondbrain
```

Re-enable it later with:

```bash
sudo systemctl enable --now secondbrain
```

The bot and local Telegram Bot API can be stopped independently. Stop the bot
first, then the Bot API if you also want to stop large-file downloads:

```bash
sudo systemctl stop secondbrain
sudo systemctl stop telegram-bot-api
```

Stopping services does not remove the database, uploaded files, keys, or logs.
The nightly backup timer is separate; disable it when deliberately keeping the
app stopped:

```bash
sudo systemctl disable --now secondbrain-backup.timer
```

## Remove Second Brain from the VM

This permanently deletes the app's local database, encrypted objects, temporary
files, credentials, and local Telegram Bot API cache. First copy any data and
the age private key you need to keep to secure offline storage, and verify the
copy. Without the age key, encrypted local objects and backups cannot be read.
This procedure removes Second Brain only; it does not delete the Azure VM,
Tailscale, the GitHub repository, a remote R2 bucket, or other Azure resources.

Run on the VM after confirming you want to erase the app data:

```bash
sudo systemctl disable --now secondbrain
sudo systemctl disable --now secondbrain-backup.timer
sudo systemctl stop secondbrain-backup.service telegram-bot-api.service || true
sudo systemctl disable telegram-bot-api.service

sudo rm -f /etc/systemd/system/secondbrain.service
sudo rm -f /etc/systemd/system/telegram-bot-api.service
sudo rm -f /etc/systemd/system/secondbrain-backup.service
sudo rm -f /etc/systemd/system/secondbrain-backup.timer
sudo rm -rf /etc/systemd/system/secondbrain.service.d
sudo rm -rf /etc/systemd/system/telegram-bot-api.service.d
sudo rm -f /etc/systemd/journald.conf.d/secondbrain.conf
sudo systemctl daemon-reload
sudo systemctl restart systemd-journald
sudo systemctl reset-failed

# Only if GitHub Actions deploy was configured:
sudo rm -f /etc/sudoers.d/secondbrain-deploy
sudo rm -f /usr/local/sbin/secondbrain-deploy

# Irreversible app data, secrets, binaries, and Bot API cache removal:
sudo rm -rf /var/lib/secondbrain
sudo rm -rf /var/lib/telegram-bot-api
sudo rm -rf /etc/secondbrain
sudo rm -rf /opt/secondbrain
sudo userdel secondbrain
```

The Ubuntu packages used for PDF/OCR (for example Poppler and Tesseract), the
VM swapfile, firewall configuration, and Tailscale installation are left in
place because they are host-level resources and may be used by other services.

The Telegram bot itself still exists in Telegram until you revoke/delete it
through BotFather. Revoke provider API keys you no longer use. If file storage
was ever switched to R2, separately delete the bucket objects and revoke its
access key in Cloudflare. GitHub Actions secrets and the dedicated SSH key are
separate: delete the repository secrets, remove the public deploy key from the
VM user's `~/.ssh/authorized_keys`, and remove the Tailscale CI OAuth client or
policy access if it is no longer used.

## Stop or delete the Azure VM itself

To stop the entire machine from the Azure portal, open **Virtual machines**,
select the VM, choose **Stop**, and wait for **Stopped (deallocated)**. A guest
shutdown or **Stopped (allocated)** state can continue compute billing;
deallocated VMs stop compute charges, while attached disks and some networking
resources may still incur charges. See Microsoft's [VM billing states](https://learn.microsoft.com/en-us/azure/virtual-machines/states-billing).

To permanently delete the VM, first back up the data you want to keep. In the
Azure portal choose the VM → **Delete**, then review the delete options for the
OS disk, data disks, and network interface. Depending on those options, disks
or networking resources can remain after the VM resource is deleted. Review
Microsoft's [delete VM and attached resources guide](https://learn.microsoft.com/en-us/azure/virtual-machines/delete).

## Related guides

- [Deployment setup and GitHub Actions configuration](DEPLOYMENT.md)
- [Operations, credentials, backups, and recovery](OPERATIONS.md)
- [Database restore runbook](RUNBOOK.md)
- [Acceptance checklist](ACCEPTANCE.md)
