-- T5.6: anomaly guards on evaluation
ALTER TABLE attempt_items ADD COLUMN flags TEXT NOT NULL DEFAULT '[]';   -- JSON «نیازمند بازبینی» reasons
ALTER TABLE exercises ADD COLUMN template_norm_sha256 TEXT;            -- template answer content, whitespace-insensitive; NULL = unknown
