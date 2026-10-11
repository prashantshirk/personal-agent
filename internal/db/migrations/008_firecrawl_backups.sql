CREATE TABLE firecrawl_usage (
  day TEXT PRIMARY KEY,
  calls INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE backups (
  object_key TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  size INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX backups_created_idx ON backups(created_at);
