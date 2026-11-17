-- T9.15: searchable text of each topic's current lesson; rowid = topics.id, body = roadmap.Normalize(lesson.md).
-- trigram matches substrings like the title search; filled on lesson commit, by rebuild and by a startup backfill.
CREATE VIRTUAL TABLE lesson_fts USING fts5(body, tokenize = 'trigram');
