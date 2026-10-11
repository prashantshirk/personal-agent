CREATE TABLE pending_transcripts (
  id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, chat_id INTEGER NOT NULL,
  text TEXT NOT NULL, created_at TEXT NOT NULL, status TEXT DEFAULT 'processed'
);
CREATE INDEX pending_transcripts_recent_idx ON pending_transcripts(chat_id,status,created_at);
