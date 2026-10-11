# Deployment and VM preparation

These commands are for the Ubuntu VM described in `DESIGN.md` (2 vCPU, 1 GB RAM). They were not run from this workspace. Review the provider's firewall and SSH access before enabling UFW.

## One-time VM preparation

1. Add a 2 GB swapfile and lower swappiness as an OCR safety margin:

   ```bash
   sudo fallocate -l 2G /swapfile
   sudo chmod 600 /swapfile
   sudo mkswap /swapfile
   sudo swapon /swapfile
   echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
   echo 'vm.swappiness=20' | sudo tee /etc/sysctl.d/99-swap.conf
   sudo sysctl --system
   ```

2. Create the service identity and directories:

   ```bash
   sudo useradd --system --create-home --home-dir /var/lib/secondbrain --shell /usr/sbin/nologin secondbrain
   sudo mkdir -p /etc/secondbrain /var/lib/secondbrain/data /var/lib/secondbrain/tmp /opt/secondbrain
   sudo chown -R secondbrain:secondbrain /var/lib/secondbrain /opt/secondbrain
   sudo chmod 750 /etc/secondbrain
   ```

3. Install PDF/OCR and operational packages:

   ```bash
   sudo apt update
   sudo apt install -y poppler-utils tesseract-ocr tesseract-ocr-eng tesseract-ocr-hin tesseract-ocr-mar imagemagick sqlite3 ffmpeg ufw curl ca-certificates unattended-upgrades
   ```

4. Allow SSH, then enable the host firewall. Keep the Bot API and ntfy ports bound to loopback; the Cloudflare Tunnel requires no inbound firewall port:

   ```bash
   sudo ufw allow OpenSSH
   sudo ufw enable
   sudo ufw status verbose
   ```

5. Build on a machine with Go, targeting the VM architecture (`uname -m`):

   ```bash
   GOOS=linux GOARCH=amd64 go build -trimpath -o secondbrain ./cmd/secondbrain
   ```

   Use `GOARCH=arm64` for an aarch64 VM. Copy the binary to `/opt/secondbrain/secondbrain` and set it executable and owned by `secondbrain`. Avoid compiling the C++ Bot API server on a 1 GB VM; the official project build requires a C++ toolchain and can be memory intensive.

## Configure the bot and provider failover

Use [`deploy/secondbrain.env.example`](../deploy/secondbrain.env.example) as the template for `/etc/secondbrain/env`. The supported default chain is Gemini 3.5 Flash-Lite, Gemini 3.1 Flash-Lite, then Groq GPT-OSS 120B. `LLM_API_KEY` is the Gemini key; `GROQ_API_KEY` is the Groq key used by both the final LLM fallback and Whisper Large v3 transcription. The bot preserves Gemini 3 thought signatures across tool calls. The fallback order can be changed with `LLM_FALLBACK_MODELS`, using `groq:<model-id>` for Groq models.

File storage is optional. Set `FILE_STORAGE_BACKEND=local` to keep age-encrypted objects and SQLite backups under the VM's data directory; this requires an age recipient and private identity. Local storage does not protect against losing the VM or its disk, so copy encrypted backups to another device. Set `FILE_STORAGE_BACKEND=r2` only when using Cloudflare R2 credentials. With neither configured, notes, tasks, reminders, chat and voice transcription work, while file storage, OCR and backups stay disabled.

The app sends the current message and any tool-call context to the provider handling that step. If Gemini 3.5 fails, the same request is attempted with Gemini 3.1, then Groq. Once a fallback succeeds during a tool round, the bot stays on that provider for the remainder of that round. Configure provider data-use settings before sending sensitive information.

Keep the file private and restart after editing:

```bash
sudo chown root:root /etc/secondbrain/env
sudo chmod 600 /etc/secondbrain/env
sudo systemctl restart secondbrain
```

## One-time setup for GitHub Actions deployments

The workflow at `.github/workflows/deploy.yml` builds the Linux amd64 executable on a GitHub-hosted runner and reaches the VM through a short-lived Tailscale node. It transfers only the executable; `/etc/secondbrain/env`, the database and private keys stay on the VM. A successful push to `main` triggers a deployment, and `workflow_dispatch` allows a manual run.

### Install the VM-side deploy command

From Windows PowerShell in the project directory, upload the root-owned deploy helper:

```powershell
scp .\deploy\secondbrain-deploy ziglin:/tmp/secondbrain-deploy
```

On the VM, install it and permit `azureuser` to run only this fixed deploy command without a sudo password:

```bash
sudo install -o root -g root -m 0750 /tmp/secondbrain-deploy /usr/local/sbin/secondbrain-deploy
rm /tmp/secondbrain-deploy
printf '%s\n' 'azureuser ALL=(root) NOPASSWD: /usr/local/sbin/secondbrain-deploy ""' | sudo tee /etc/sudoers.d/secondbrain-deploy >/dev/null
sudo chmod 0440 /etc/sudoers.d/secondbrain-deploy
sudo visudo -cf /etc/sudoers.d/secondbrain-deploy
```

The helper installs to `/opt/secondbrain/secondbrain`, restarts the service, checks that it remains active, and restores the previous executable if startup fails. It accepts no arguments and reads the upload from `/home/azureuser/secondbrain.new`.

### Create a dedicated SSH key for Actions

On your Windows PC, create a key dedicated to deployment. Leave its passphrase empty so the workflow can use it non-interactively:

```powershell
ssh-keygen -t ed25519 -C "secondbrain-github-actions" -f "$env:USERPROFILE\.ssh\secondbrain_github_actions"
```

Append the **public** key to the VM account's authorized keys:

```powershell
Get-Content "$env:USERPROFILE\.ssh\secondbrain_github_actions.pub" | ssh ziglin "umask 077; mkdir -p ~/.ssh; cat >> ~/.ssh/authorized_keys; chmod 700 ~/.ssh; chmod 600 ~/.ssh/authorized_keys"
```

Never add the private key to the VM or repository. Add it as a GitHub repository secret in the next section.

### Configure Tailscale and GitHub

In the Tailscale admin console:

1. Create a `tag:ci` tag for ephemeral workflow runners.
2. Create an OAuth client with device and auth-key write scopes, restricted to `tag:ci`.
3. Ensure the tailnet policy allows `tag:ci` to reach this VM on SSH port 22. Preserve existing policy rules; avoid granting the runner unrestricted access to the tailnet.

In the GitHub repository, open **Settings → Secrets and variables → Actions** and add these repository secrets:

| Secret | Value |
|---|---|
| `TS_OAUTH_CLIENT_ID` | Tailscale OAuth client ID |
| `TS_OAUTH_SECRET` | Tailscale OAuth client secret |
| `VM_TAILSCALE_IP` | The VM's Tailscale IP (`tailscale ip -4` on the VM) |
| `VM_SSH_PRIVATE_KEY` | Contents of `secondbrain_github_actions` (private key) |
| `VM_SSH_KNOWN_HOSTS` | The pinned SSH host key line for the VM's Tailscale IP |

Get the host key line on the VM with:

```bash
sudo awk '{print "100.125.171.85 " $0}' /etc/ssh/ssh_host_ed25519_key.pub
```

Replace the sample IP with the current Tailscale IP. This is a public host key, used to verify the VM during deployment. Keep all API keys out of GitHub Actions; the workflow does not need them.

After adding the workflow and VM helper to the GitHub repository, push to `main`. Future pushes build and deploy automatically. For a manual deployment now, build with `make linux`, then from PowerShell run `scp .\bin\secondbrain-linux ziglin:/home/azureuser/secondbrain.new` followed by `ssh ziglin "sudo -n /usr/local/sbin/secondbrain-deploy"`.

## Local Telegram Bot API server

The local server is needed for large file downloads. The official server supports `--local`, accepts `--api-id`/`--api-hash` directly or through environment variables, and can return absolute paths for local files. It defaults to port 8081 and its default listen IP is all IPv4 interfaces, so explicitly restrict it to `127.0.0.1`. Keep the API ID/hash in a root-readable environment file, never on a command line or in chat.

1. Obtain an API ID/hash through Telegram's `my.telegram.org` developer tools and put them in `/etc/secondbrain/telegram-api.env`:

   ```ini
   TELEGRAM_API_ID=your_numeric_id
   TELEGRAM_API_HASH=your_api_hash
   ```

   ```bash
   sudo chown root:secondbrain /etc/secondbrain/telegram-api.env
   sudo chmod 640 /etc/secondbrain/telegram-api.env
   sudo mkdir -p /var/lib/telegram-bot-api
   sudo chown secondbrain:secondbrain /var/lib/telegram-bot-api
   ```

2. Install the official prebuilt binary for the VM architecture if available from a trusted release/build process. The upstream README's reproducible build route uses its build-instructions generator or CMake source build. Verify the binary provenance and check `telegram-bot-api --help` for the current flags before installing.

3. Create `/etc/systemd/system/telegram-bot-api.service`:

   ```ini
   [Unit]
   Description=Telegram Local Bot API server
   After=network-online.target
   Wants=network-online.target

   [Service]
   Type=simple
   User=secondbrain
   Group=secondbrain
   EnvironmentFile=/etc/secondbrain/telegram-api.env
   WorkingDirectory=/var/lib/telegram-bot-api
   ExecStart=/usr/local/bin/telegram-bot-api --local --http-ip-address=127.0.0.1 --http-port=8081 --dir=/var/lib/telegram-bot-api
   Restart=on-failure
   NoNewPrivileges=true
   ProtectSystem=strict
   ProtectHome=true
   PrivateTmp=true
   PrivateDevices=true
   ProtectKernelTunables=true
   ProtectKernelModules=true
   ProtectControlGroups=true
   LockPersonality=true
   RestrictSUIDSGID=true
   CapabilityBoundingSet=
   RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
   ReadWritePaths=/var/lib/telegram-bot-api
   MemoryHigh=150M
   MemoryMax=300M

   [Install]
   WantedBy=multi-user.target
   ```

   The official server reads the API ID/hash from `TELEGRAM_API_ID` and `TELEGRAM_API_HASH` environment variables. Verify `systemctl status` before switching the bot.

4. Enable and verify the local server:

   ```bash
   sudo systemctl daemon-reload
   sudo systemctl enable --now telegram-bot-api
   sudo systemctl status telegram-bot-api
   sudo ss -ltnp | grep 8081
   ```

   The listener must show `127.0.0.1:8081`, not `0.0.0.0:8081`. In `/etc/secondbrain/env`, set `TELEGRAM_API_BASE=http://127.0.0.1:8081` and `TELEGRAM_FILE_CACHE_DIR=/var/lib/telegram-bot-api`. The bot copies an absolute local file path directly and removes the cached file only when it is inside this configured directory.

5. When ready to switch from Telegram's cloud API, stop the bot first. Use `logOut` against the cloud endpoint with the token in a private shell (not in chat):

   ```bash
   read -rsp 'Bot token: ' TELEGRAM_BOT_TOKEN; echo
   curl --fail --silent --show-error "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/logOut"
   unset TELEGRAM_BOT_TOKEN
   ```

   A successful call returns `{"ok":true,"result":true}`. Telegram documents that you may then log in locally immediately, but cannot log back into the cloud API for 10 minutes. Start the local server, then the bot configured to use `http://127.0.0.1:8081`. Do not keep the same bot logged into two local servers concurrently.

## Configure local encrypted file storage

The no-cloud option stores age-encrypted files and database backups under `/var/lib/secondbrain/data/objects`. It uses VM disk space and is not a disaster backup by itself. Keep an offline copy of the private identity and copy encrypted backups to another device.

```bash
sudo apt install -y age
sudo install -d -o secondbrain -g secondbrain -m 700 /etc/secondbrain
sudo -u secondbrain age-keygen -o /etc/secondbrain/age.key
sudo chmod 600 /etc/secondbrain/age.key
sudo chown secondbrain:secondbrain /etc/secondbrain/age.key
sudo grep '^# public key:' /etc/secondbrain/age.key
```

Copy the displayed public recipient into `AGE_RECIPIENT` in `/etc/secondbrain/env`, then set:

```ini
FILE_STORAGE_BACKEND=local
LOCAL_STORAGE_DIR=/var/lib/secondbrain/data/objects
AGE_IDENTITY_FILE=/etc/secondbrain/age.key
```

Make an offline backup of the **private** identity using a secure password manager or encrypted removable storage, and verify that backup can be read before enabling uploads. Do not paste either key into chat. Loss of every private-key copy makes encrypted objects and database backups permanently unreadable.

If you later configure R2, set `FILE_STORAGE_BACKEND=r2` and its five `R2_*` settings instead. Never configure both backends as active at once; each object key belongs to the backend where it was created.

6. Install the bot and backup systemd units from `deploy/systemd`, then:

   ```bash
   sudo mkdir -p /etc/systemd/journald.conf.d
   sudo install -m 0644 deploy/systemd/secondbrain.service /etc/systemd/system/
   sudo install -m 0644 deploy/systemd/secondbrain-backup.service /etc/systemd/system/
   sudo install -m 0644 deploy/systemd/secondbrain-backup.timer /etc/systemd/system/
   sudo install -m 0644 deploy/systemd/journald-secondbrain.conf /etc/systemd/journald.conf.d/secondbrain.conf
   sudo systemctl daemon-reload
   sudo systemctl restart systemd-journald
   sudo systemctl enable --now secondbrain secondbrain-backup.timer
   ```

   Put service secrets directly in `/etc/secondbrain/env`, mode `0600`; do not send them here. A local `--backup` run and live Telegram acceptance require the relevant credentials and are tracked in `docs/ACCEPTANCE.md`.
