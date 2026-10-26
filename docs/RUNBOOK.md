# Restore a Second Brain database

The database is SQLite at `/var/lib/secondbrain/data/secondbrain.db` — it is not Cloudflare R2. The encrypted backup is stored by the active storage backend under `backups/`. On the deployed VM (`FILE_STORAGE_BACKEND=local`) that is `/var/lib/secondbrain/data/objects/backups/<object>.db.age`. Only if the backend is `r2` is the backup in the private R2 bucket under `backups/`. The age identity at `/etc/secondbrain/age.key` is required to decrypt it. Keep an offline copy of that identity; without it, the database backup cannot be read.

1. SSH to the VM and stop the bot: `sudo systemctl stop secondbrain`.
2. Confirm the bot is stopped: `sudo systemctl is-active secondbrain` should print `inactive`.
3. Choose the newest backup object key from the `backups` table before a full VM loss, or use the key printed by `/backup`. Never post the object key with credentials.
4. Move the current database aside: `sudo -u secondbrain mv /var/lib/secondbrain/data/secondbrain.db /var/lib/secondbrain/data/secondbrain.db.pre-restore`. Also move any `secondbrain.db-wal` and `secondbrain.db-shm` files aside if they exist.
5. Restore and verify into the expected database path: `sudo -u secondbrain /opt/secondbrain/secondbrain --restore 'backups/<object>.db.age' /var/lib/secondbrain/data/secondbrain.db`. The command decrypts with age, runs `PRAGMA integrity_check`, and refuses to overwrite an existing destination.
6. Check ownership and permissions: `sudo chown secondbrain:secondbrain /var/lib/secondbrain/data/secondbrain.db && sudo chmod 600 /var/lib/secondbrain/data/secondbrain.db`.
7. Start the bot: `sudo systemctl start secondbrain`.
8. Verify startup and migrations: `sudo systemctl --no-pager --full status secondbrain` and `sudo journalctl -u secondbrain -n 100 --no-pager`.
9. In Telegram, send `/find` with a word you know exists in a saved note or indexed file. Confirm that notes, file rows, and search results appear.
10. Keep the pre-restore database and WAL/SHM files until the restore has passed verification. Remove those old files only after confirming the restored bot works.

For a scratch restore, choose a new destination path outside the live data directory and run the same `--restore` command. Open it with `sqlite3 <path> 'PRAGMA integrity_check;'` and search the FTS tables. Do not point the running service at a scratch database.

The program can restore an existing backup object, but it does not yet provide a command to list backups. Record the key printed by `/backup`, query the `backups` table in SQLite, or — for the local backend — list `/var/lib/secondbrain/data/objects/backups/`. Only for the R2 backend, inspect the private bucket in the Cloudflare dashboard.
