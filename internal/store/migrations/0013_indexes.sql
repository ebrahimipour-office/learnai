-- T13.9: correlated "latest evaluated answer" subqueries filter attempt_items by exercise_id;
-- File() looks up exercise_sets by rubric_file; job stats filter jobs by finished_at.
CREATE INDEX IF NOT EXISTS idx_attempt_items_exercise ON attempt_items(exercise_id, status);
CREATE INDEX IF NOT EXISTS idx_exercise_sets_rubric ON exercise_sets(rubric_file);
CREATE INDEX IF NOT EXISTS idx_jobs_finished ON jobs(finished_at);
