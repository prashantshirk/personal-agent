CREATE TABLE schedules (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  chat_id INTEGER NOT NULL,
  kind TEXT NOT NULL,
  prompt TEXT NOT NULL DEFAULT '',
  file_id INTEGER,
  next_at TEXT NOT NULL,
  repeat_rule TEXT,
  status TEXT NOT NULL DEFAULT 'scheduled',
  attempts INTEGER NOT NULL DEFAULT 0,
  last_error TEXT,
  created_at TEXT NOT NULL
);
CREATE INDEX schedules_due_idx ON schedules(status, next_at);
