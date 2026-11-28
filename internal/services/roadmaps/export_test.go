package roadmaps

// DiffRuns is how many diffs were computed rather than read from import_drafts.diff (T13.11).
func DiffRuns() int64 { return diffRuns.Load() }
