CREATE TABLE pending_actions (
  id INTEGER PRIMARY KEY, kind TEXT NOT NULL, payload TEXT NOT NULL,
  created_at TEXT NOT NULL, expires_at TEXT NOT NULL, status TEXT DEFAULT 'pending'
);
CREATE INDEX pending_actions_lookup_idx ON pending_actions(status,expires_at);
