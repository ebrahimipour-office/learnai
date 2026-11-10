-- T8.3 (design §13.4): a topic of the tree rule points at the nearest kept topic above it. NULL = top level.
ALTER TABLE topics ADD COLUMN parent_id INTEGER REFERENCES topics(id);
CREATE INDEX idx_topics_parent ON topics(parent_id);
