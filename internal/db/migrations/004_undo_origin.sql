ALTER TABLE notes ADD COLUMN origin_inbox_id INTEGER;
ALTER TABLE tasks ADD COLUMN origin_inbox_id INTEGER;
ALTER TABLE reminders ADD COLUMN origin_inbox_id INTEGER;
CREATE INDEX notes_origin_idx ON notes(origin_inbox_id);
CREATE INDEX tasks_origin_idx ON tasks(origin_inbox_id);
CREATE INDEX reminders_origin_idx ON reminders(origin_inbox_id);
