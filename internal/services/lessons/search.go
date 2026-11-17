package lessons

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"

	"learninghub/internal/fsx"
	"learninghub/internal/roadmap"
	"learninghub/internal/store"
)

// IndexText makes md the searchable text of topicID's current lesson (T9.15).
func IndexText(ctx context.Context, tx *sql.Tx, topicID int64, md []byte) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM lesson_fts WHERE rowid = ?`, topicID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO lesson_fts (rowid, body) VALUES (?, ?)`, topicID, roadmap.Normalize(string(md)))
	return err
}

// IndexMissing indexes the current lessons that have no search text yet: those committed before
// migration 0007. A lesson file that is gone is skipped; sync reports it.
func IndexMissing(ctx context.Context, db *store.DB, root *fsx.Root) (int, error) {
	rows, err := db.Writer().QueryContext(ctx, `SELECT v.topic_id, f.rel_path FROM lesson_versions v JOIN files f ON f.id = v.file_id
		WHERE v.is_current = 1 AND v.topic_id NOT IN (SELECT rowid FROM lesson_fts)`)
	if err != nil {
		return 0, err
	}
	type lesson struct {
		topic int64
		rel   string
	}
	var todo []lesson
	for rows.Next() {
		var l lesson
		if err := rows.Scan(&l.topic, &l.rel); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, l := range todo {
		md, err := root.ReadFile(l.rel)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return n, err
		}
		if err := db.WithTx(ctx, func(tx *sql.Tx) error { return IndexText(ctx, tx, l.topic, md) }); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
