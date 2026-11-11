package roadmaps

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"learninghub/internal/fsx"
	"learninghub/internal/roadmap"
)

var ErrNothingSelected = errors.New("no topics selected")

// ErrTooManyTopics: a tree selection keeps more than Service.TreeCap records; the user must untick some (R12).
var ErrTooManyTopics = errors.New("too many topics for the tree rule")

// MaxTreeTopics is the default of config limits.max_tree_topics (Service.TreeCap): it caps the records
// one tree import creates, so a heading-dense file cannot flood topics/ (R12).
const MaxTreeTopics = 200

// maxDirSlugRunes keeps topics/<NN-slug>/... well under fsx's 220-byte path limit for Persian titles.
const maxDirSlugRunes = 40

// Confirm records the draft (T2.5, T2.7) in one transaction: the roadmap (new import) or its next
// revision (re-import), groups and topics, plus roadmaps/<slug>/rev-NNN.md written with WriteNew.
// Excluded topics are skipped, and so are groups left without topics. On re-import, topics are
// matched by stable key and updated in place (dir_name never changes); recorded topics and groups
// that are no longer kept are only archived, never deleted. The draft is consumed. Each topic with a
// source range gets its chapter text in topics/<dir>/source.md (§12.6). On any failure no rows change
// and no file written by this confirm is left behind.
func (s *Service) Confirm(ctx context.Context, id string) (roadmapID int64, topics int, err error) {
	d, err := s.Draft(ctx, id)
	if err != nil {
		return 0, 0, err
	}
	sel, err := d.Preview(d.Choice)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: %v", ErrInvalidSource, err)
	}
	kept := KeptTopics(sel, d.Choice.Excluded)
	if len(kept) == 0 {
		return 0, 0, ErrNothingSelected
	}
	if d.Choice.Rule.Kind == roadmap.RuleTree && len(kept) > s.TreeCap {
		return 0, 0, ErrTooManyTopics
	}
	usedGroup := make([]bool, len(sel.Groups))
	var groupKeys []string
	for _, t := range kept {
		for g := t.Group; g >= 0 && !usedGroup[g]; g = sel.Groups[g].Parent {
			usedGroup[g] = true
			groupKeys = append(groupKeys, sel.Groups[g].StableKey)
		}
	}
	topicKeys := make([]string, len(kept))
	for i, t := range kept {
		topicKeys[i] = t.StableKey
	}

	tree, err := json.Marshal(roadmap.Parse(d.Content))
	if err != nil {
		return 0, 0, err
	}
	rule, err := json.Marshal(d.Choice.Rule)
	if err != nil {
		return 0, 0, err
	}
	keysJSON := func(keys []string) string { b, _ := json.Marshal(append([]string{}, keys...)); return string(b) }
	var undo []func() // reverts each file write done so far, so a failed commit leaves the disk as it was

	err = s.db.WithTx(ctx, func(tx *sql.Tx) error {
		// Deleting first makes a second concurrent confirm of the same draft fail instead of importing twice.
		res, err := tx.ExecContext(ctx, `DELETE FROM import_drafts WHERE id = ? AND expires_at > ?`, id, s.ts(s.Now()))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrDraftNotFound
		}

		var slug string
		revNo := 1
		if d.RoadmapID == 0 {
			title := cmp.Or(d.Title, d.DefaultTitle())
			// the folder keeps the file name: a title edit must not rename roadmaps/<slug>
			if slug, err = s.freeRoadmapSlug(ctx, tx, strings.TrimSuffix(d.SourceName, filepath.Ext(d.SourceName))); err != nil {
				return err
			}
			res, err = tx.ExecContext(ctx, `INSERT INTO roadmaps (slug, title, select_rule) VALUES (?, ?, ?)`, slug, title, string(rule))
			if err != nil {
				return fmt.Errorf("inserting roadmap: %w", err)
			}
			if roadmapID, err = res.LastInsertId(); err != nil {
				return err
			}
		} else {
			roadmapID = d.RoadmapID
			if err := tx.QueryRowContext(ctx, `SELECT r.slug, COALESCE(MAX(v.rev_no), 0) + 1 FROM roadmaps r
				LEFT JOIN roadmap_revisions v ON v.roadmap_id = r.id WHERE r.id = ? GROUP BY r.id`, roadmapID).Scan(&slug, &revNo); err != nil {
				return fmt.Errorf("loading roadmap %d: %w", roadmapID, err)
			}
			// the title changes only when the user ticked "replace title" in the preview (§12.2)
			if _, err := tx.ExecContext(ctx, `UPDATE roadmaps SET select_rule = ?, title = COALESCE(?, title) WHERE id = ?`,
				string(rule), sql.NullString{String: d.Title, Valid: d.Title != ""}, roadmapID); err != nil {
				return err
			}
		}
		rel := fmt.Sprintf("roadmaps/%s/rev-%03d.md", slug, revNo)
		res, err = tx.ExecContext(ctx,
			`INSERT INTO roadmap_revisions (roadmap_id, rev_no, rel_path, sha256, parsed_tree) VALUES (?, ?, ?, ?, ?)`,
			roadmapID, revNo, rel, fsx.HashBytes(d.Content), string(tree))
		if err != nil {
			return fmt.Errorf("inserting revision: %w", err)
		}
		revID, err := res.LastInsertId()
		if err != nil {
			return err
		}

		groupID := make([]sql.NullInt64, len(sel.Groups)) // parents precede children in sel.Groups
		for i, g := range sel.Groups {
			if !usedGroup[i] {
				continue
			}
			var parent sql.NullInt64
			if g.Parent >= 0 {
				parent = groupID[g.Parent]
			}
			var gid int64
			err := tx.QueryRowContext(ctx,
				`INSERT INTO groups (roadmap_id, parent_id, stable_key, title, level, ord) VALUES (?, ?, ?, ?, ?, ?)
				 ON CONFLICT (roadmap_id, stable_key) DO UPDATE SET parent_id = excluded.parent_id, title = excluded.title,
				   level = excluded.level, ord = excluded.ord, state = 'active'
				 RETURNING id`,
				roadmapID, parent, g.StableKey, g.Title, g.Level, float64(i+1)).Scan(&gid)
			if err != nil {
				return fmt.Errorf("recording group %q: %w", g.Title, err)
			}
			groupID[i] = sql.NullInt64{Int64: gid, Valid: true}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE groups SET state = 'archived'
			WHERE roadmap_id = ? AND state = 'active' AND stable_key NOT IN (SELECT value FROM json_each(?))`,
			roadmapID, keysJSON(groupKeys)); err != nil {
			return fmt.Errorf("archiving groups: %w", err)
		}

		old, err := recordedTopics(ctx, tx, roadmapID)
		if err != nil {
			return err
		}
		oldByKey := make(map[string]oldTopic, len(old))
		for _, o := range old {
			oldByKey[o.Key] = o
		}
		var writes []sourceWrite
		ids := make([]int64, len(kept)) // kept index -> topics.id; a parent precedes its children
		// Topics are never deleted (only archived), so the count is a gap-free global sequence
		// that keeps dir_name unique across roadmaps.
		var seq int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM topics`).Scan(&seq); err != nil {
			return err
		}
		for i, t := range kept {
			var group sql.NullInt64
			if t.Group >= 0 {
				group = groupID[t.Group]
			}
			outline := sql.NullString{String: t.Outline, Valid: t.Outline != ""}
			var parent sql.NullInt64
			if t.Parent >= 0 {
				parent = sql.NullInt64{Int64: ids[t.Parent], Valid: true}
			}
			o, ok := oldByKey[t.StableKey]
			if ok {
				_, err := tx.ExecContext(ctx,
					`UPDATE topics SET group_id = ?, parent_id = ?, title = ?, title_norm = ?, outline = ?, ord = ?, state = 'active',
					   source_rev_id = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
					group, parent, t.Title, roadmap.Normalize(t.Title), outline, float64(i+1), revID, o.ID)
				if err != nil {
					return fmt.Errorf("updating topic %q: %w", t.Title, err)
				}
			} else {
				seq++
				slug, dir := TopicDir(seq, t.Title)
				res, err := tx.ExecContext(ctx,
					`INSERT INTO topics (roadmap_id, group_id, parent_id, stable_key, title, title_norm, outline, ord, slug, dir_name, source_rev_id)
					 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					roadmapID, group, parent, t.StableKey, t.Title, roadmap.Normalize(t.Title), outline,
					float64(i+1), slug, dir, revID)
				if err != nil {
					return fmt.Errorf("inserting topic %q: %w", t.Title, err)
				}
				if o.ID, err = res.LastInsertId(); err != nil {
					return err
				}
				o.Dir = dir
			}
			ids[i] = o.ID
			if t.EndLine > 0 {
				w, err := s.recordSource(ctx, tx, o, t, d.Content)
				if err != nil {
					return fmt.Errorf("recording source of %q: %w", t.Title, err)
				}
				if w != nil {
					writes = append(writes, *w)
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE topics SET state = 'archived', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE roadmap_id = ? AND origin = 'roadmap' AND state = 'active' AND stable_key NOT IN (SELECT value FROM json_each(?))`,
			roadmapID, keysJSON(topicKeys)); err != nil {
			return fmt.Errorf("archiving topics: %w", err)
		}

		// Files go last: any earlier failure leaves nothing on disk. Files created with O_EXCL are ours to remove.
		if err := s.root.WriteNew(rel, d.Content, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", rel, err)
		}
		undo = append(undo, func() { _ = s.root.Remove(rel) })
		// The choice beside the revision lets hub rebuild re-select the same topics (CR-19).
		choiceRel := ChoiceRel(rel)
		choice, err := json.Marshal(Choice{Rule: d.Choice.Rule, Excluded: d.Choice.Excluded})
		if err != nil {
			return err
		}
		if err := s.root.WriteNew(choiceRel, choice, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", choiceRel, err)
		}
		undo = append(undo, func() { _ = s.root.Remove(choiceRel) })
		for _, w := range writes {
			if err := s.writeSource(w, &undo); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
		return 0, 0, err
	}
	return roadmapID, len(kept), nil
}

// sourceWrite is a chapter text to put on disk once its rows are recorded. hist != "" means source.md
// exists and its current bytes (prev) move to hist first.
type sourceWrite struct {
	rel, hist  string
	data, prev []byte
}

// recordSource records the chapter text of topic o (range t.Line..t.EndLine of content) and returns the
// file write it needs, or nil when the text is unchanged since the last import (§12.6).
func (s *Service) recordSource(ctx context.Context, tx *sql.Tx, o oldTopic, t roadmap.Topic, content []byte) (*sourceWrite, error) {
	data := roadmap.Slice(content, t.Line, t.EndLine)
	sha := fsx.HashBytes(data)
	if _, err := tx.ExecContext(ctx, `UPDATE topics SET source_start_line = ?, source_end_line = ?, source_sha256 = ? WHERE id = ?`,
		t.Line, t.EndLine, sha, o.ID); err != nil {
		return nil, err
	}
	if sha == o.SourceSHA {
		return nil, nil // same text, maybe moved: the file (and any user edit of it) stays
	}
	w := &sourceWrite{rel: path.Join("topics", o.Dir, "source.md"), data: data}
	if !o.SourceFile.Valid {
		res, err := tx.ExecContext(ctx, `INSERT INTO files (topic_id, kind, owner_type, owner_id, rel_path, sha256, size_bytes, observed_sha256)
			VALUES (?, 'source', 'topic', ?, ?, ?, ?, ?)`, o.ID, o.ID, w.rel, sha, len(data), sha)
		if err != nil {
			return nil, err
		}
		fileID, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE topics SET source_file_id = ? WHERE id = ?`, fileID, o.ID)
		return w, err
	}
	// Keep what is on disk now (the user may have edited it) before replacing it (I1).
	prev, err := s.root.ReadFile(w.rel)
	switch {
	case err == nil:
		var n int // rows of this topic's source files = source.md + earlier history, so n numbers the next one
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE topic_id = ? AND kind = 'source'`, o.ID).Scan(&n); err != nil {
			return nil, err
		}
		w.hist, w.prev = path.Join("topics", o.Dir, "source-history", fmt.Sprintf("source.%04d.md", n)), prev
		ph := fsx.HashBytes(prev)
		if _, err := tx.ExecContext(ctx, `INSERT INTO files (topic_id, kind, owner_type, owner_id, rel_path, sha256, size_bytes, observed_sha256)
			VALUES (?, 'source', 'topic', ?, ?, ?, ?, ?)`, o.ID, o.ID, w.hist, ph, len(prev), ph); err != nil {
			return nil, err
		}
	case !errors.Is(err, fs.ErrNotExist): // a missing source.md is just written anew
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE files SET sha256 = ?, size_bytes = ?, observed_sha256 = ?, observed_mtime = NULL,
		observed_at = NULL, presence = 'present' WHERE id = ?`, sha, len(data), sha, o.SourceFile.Int64)
	return w, err
}

// writeSource puts w on disk and pushes its revert onto undo. A replaced source.md is restored before
// its history copy is removed, so the old text is never lost.
func (s *Service) writeSource(w sourceWrite, undo *[]func()) error {
	if w.hist == "" {
		if err := s.root.WriteNew(w.rel, w.data, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", w.rel, err)
		}
		*undo = append(*undo, func() { _ = s.root.Remove(w.rel) })
		return nil
	}
	if err := s.root.WriteNew(w.hist, w.prev, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", w.hist, err)
	}
	if err := s.root.WriteAtomic(w.rel, w.data, 0o644); err != nil {
		_ = s.root.Remove(w.hist)
		return fmt.Errorf("writing %s: %w", w.rel, err)
	}
	*undo = append(*undo, func() {
		if s.root.WriteAtomic(w.rel, w.prev, 0o644) == nil {
			_ = s.root.Remove(w.hist)
		}
	})
	return nil
}

// freeRoadmapSlug picks slug, slug-2, ... unused both in the DB and on disk.
func (s *Service) freeRoadmapSlug(ctx context.Context, tx *sql.Tx, title string) (string, error) {
	base := slugOr(title, "roadmap", 0)
	for n := 1; ; n++ {
		slug := base
		if n > 1 {
			slug = fmt.Sprintf("%s-%d", base, n)
		}
		var taken int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM roadmaps WHERE slug = ?`, slug).Scan(&taken); err != nil {
			return "", err
		}
		if taken == 0 && !s.root.Exists("roadmaps/"+slug) {
			return slug, nil
		}
	}
}

// ChoiceRel is the file holding the Choice confirmed with revision rel: roadmaps/<slug>/rev-NNN.choice.json.
func ChoiceRel(rel string) string { return strings.TrimSuffix(rel, ".md") + ".choice.json" }

// TopicDir returns the slug and dir_name (NN-slug) of the seq-th topic ever created. seq must be
// COUNT(*) of topics + 1 inside the inserting transaction: topics are never deleted, so it is unique.
func TopicDir(seq int, title string) (slug, dir string) {
	slug = slugOr(title, "topic", maxDirSlugRunes)
	return slug, fmt.Sprintf("%02d-%s", seq, slug)
}

// slugOr returns fsx.SanitizeSlug(s) cut to maxRunes (0 = no cut), or fallback when s has no usable characters.
func slugOr(s, fallback string, maxRunes int) string {
	slug, err := fsx.SanitizeSlug(s)
	if err != nil {
		return fallback
	}
	if r := []rune(slug); maxRunes > 0 && len(r) > maxRunes {
		slug = strings.Trim(string(r[:maxRunes]), "-. ")
	}
	return slug
}
