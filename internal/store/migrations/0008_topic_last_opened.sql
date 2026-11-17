-- T10.4: when the topic page was last opened, for the "continue where you left off" card on /topics. NULL = never.
ALTER TABLE topics ADD COLUMN last_opened_at TEXT;
