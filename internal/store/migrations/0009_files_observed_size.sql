-- T10.7: size of the file when observed_sha256/observed_mtime were taken; sync reuses the hash while size and mtime still match. NULL = re-hash.
ALTER TABLE files ADD COLUMN observed_size INTEGER;
