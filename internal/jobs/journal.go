package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"

	"learninghub/internal/fsx"
)

// JournalEntry is one file a commit writes (CR-03). RestoreFrom names the copy of the previous
// content when the commit overwrites a file that already has a files row (lesson regeneration);
// it must come before the entry for that copy in the journal.
type JournalEntry struct {
	RelPath     string `json:"rel_path"`
	SHA256      string `json:"sha256"`
	RestoreFrom string `json:"restore_from,omitempty"`
}

// replayJournals undoes the disk half of commits whose DB transaction never landed: written files
// with no files row are removed, an overwritten file is restored from its copy. A file whose
// content differs from the journal is never touched; it is noted on the job instead.
func (r *Runner) replayJournals(ctx context.Context) error {
	rows, err := r.db.Writer().QueryContext(ctx,
		`SELECT id, commit_journal FROM jobs WHERE commit_journal IS NOT NULL AND status != 'succeeded'`)
	if err != nil {
		return err
	}
	type pending struct {
		id      int64
		journal string
	}
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.journal); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, p)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, p := range todo {
		// One bad journal must not keep the app from starting; it stays set and is retried next start.
		if err := r.replayJournal(ctx, p.id, p.journal); err != nil {
			slog.Error("Replaying commit journal failed", "job", p.id, "error", err)
		}
	}
	return nil
}

func (r *Runner) replayJournal(ctx context.Context, id int64, journal string) error {
	var entries []JournalEntry
	if err := json.Unmarshal([]byte(journal), &entries); err != nil {
		return err
	}
	var kept []string
	for _, e := range entries {
		ok, err := r.replayEntry(ctx, e)
		if err != nil {
			return fmt.Errorf("%s: %w", e.RelPath, err)
		}
		if !ok {
			kept = append(kept, e.RelPath)
			slog.Warn("Journal file differs from the commit; kept", "job", id, "path", e.RelPath)
		}
	}
	note := ""
	if len(kept) > 0 {
		note = " — فایل تغییرکرده نگه داشته شد: " + strings.Join(kept, ", ")
	}
	_, err := r.db.Writer().ExecContext(ctx, `UPDATE jobs SET commit_journal = NULL,
		error_message = CASE WHEN ?1 = '' THEN error_message ELSE COALESCE(error_message, '') || ?1 END WHERE id = ?2`,
		note, id)
	return err
}

// replayEntry reconciles one file; false means it was left in place because its content changed.
func (r *Runner) replayEntry(ctx context.Context, e JournalEntry) (bool, error) {
	disk, _, err := r.Files.HashFile(e.RelPath)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil // never written, or already undone
	}
	if err != nil {
		return false, err
	}
	var row string
	err = r.db.Writer().QueryRowContext(ctx, `SELECT sha256 FROM files WHERE rel_path = ?`, e.RelPath).Scan(&row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if disk != e.SHA256 {
			return false, nil
		}
		return true, r.Files.Remove(e.RelPath)
	case err != nil:
		return false, err
	case e.RestoreFrom == "" || row == e.SHA256 || disk == row:
		return true, nil // committed, or never overwritten
	case disk != e.SHA256:
		return false, nil
	}
	prev, err := r.Files.ReadFile(e.RestoreFrom)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err != nil || fsx.HashBytes(prev) != row {
		return false, nil // no faithful copy of the old content: keep what is there
	}
	return true, r.Files.WriteAtomic(e.RelPath, prev, 0o644)
}
