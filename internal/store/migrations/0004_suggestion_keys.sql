-- CR-01: accepted suggestions got stable_key 'suggestion-N', which fails the manifest/rubric/result
-- schemas (^[0-9a-f]{16}$), so those topics could never get exercises. The key is opaque; SQLite has no
-- sha256, so old rows get the zero-padded hex topic id (new rows: a hash, see topics.AcceptSuggestion).
UPDATE topics SET stable_key = printf('%016x', id) WHERE stable_key LIKE 'suggestion-%';
