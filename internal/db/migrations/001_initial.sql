CREATE TABLE inbox (
  id INTEGER PRIMARY KEY, received_at TEXT NOT NULL, kind TEXT NOT NULL,
  raw TEXT, handled INTEGER DEFAULT 0, error TEXT
);
CREATE TABLE notes (
  id INTEGER PRIMARY KEY, created_at TEXT NOT NULL, body TEXT NOT NULL,
  tags TEXT DEFAULT '', source TEXT DEFAULT 'text'
);
CREATE TABLE tasks (
  id INTEGER PRIMARY KEY, created_at TEXT NOT NULL, body TEXT NOT NULL,
  due_at TEXT, status TEXT DEFAULT 'open', done_at TEXT
);
CREATE TABLE reminders (
  id INTEGER PRIMARY KEY, body TEXT NOT NULL, due_at TEXT NOT NULL,
  repeat_rule TEXT, critical INTEGER DEFAULT 0, status TEXT DEFAULT 'scheduled',
  next_attempt_at TEXT, attempts INTEGER DEFAULT 0, acked_at TEXT, snoozed_until TEXT
);
CREATE INDEX reminders_due_idx ON reminders(status,due_at);
CREATE TABLE files (
  id INTEGER PRIMARY KEY, created_at TEXT NOT NULL, name TEXT NOT NULL,
  mime TEXT, size INTEGER, sha256 TEXT UNIQUE, r2_key TEXT NOT NULL,
  encrypted INTEGER DEFAULT 1, caption TEXT DEFAULT '', tags TEXT DEFAULT '',
  index_status TEXT DEFAULT 'pending', pages INTEGER, ocr_pages INTEGER
);
CREATE TABLE file_pages (
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  page INTEGER NOT NULL, source TEXT, body TEXT, PRIMARY KEY(file_id,page)
);
CREATE VIRTUAL TABLE search_fts USING fts5(title,tags,body,kind UNINDEXED,ref_id UNINDEXED,page UNINDEXED,
  tokenize='unicode61 remove_diacritics 2');
CREATE VIRTUAL TABLE search_tri USING fts5(title,tags,body,kind UNINDEXED,ref_id UNINDEXED,page UNINDEXED,
  tokenize='trigram');
CREATE TABLE jobs (
  id INTEGER PRIMARY KEY, type TEXT NOT NULL, payload TEXT, status TEXT DEFAULT 'pending',
  attempts INTEGER DEFAULT 0, error TEXT, created_at TEXT, started_at TEXT, finished_at TEXT
);
CREATE INDEX jobs_status_idx ON jobs(status,created_at);
CREATE TABLE llm_usage (
  day TEXT PRIMARY KEY, prompt_tokens INTEGER DEFAULT 0,
  completion_tokens INTEGER DEFAULT 0, calls INTEGER DEFAULT 0
);
