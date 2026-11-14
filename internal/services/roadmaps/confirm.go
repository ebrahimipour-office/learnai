package roadmaps

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"path/filepath"
	"strconv"
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

	// Pre-slice and hash every chapter source before taking the database write lock (CR-16).
	lines := roadmap.NewLines(d.Content)
	type topicSource struct {
		data []byte
		sha  string
	}
	sources := make([]topicSource, len(kept))
	for i, t := range kept {
		if t.EndLine > 0 {
			data := lines.Slice(t.Line, t.EndLine)
			sources[i] = topicSource{
				data: data,
				sha:  fsx.HashBytes(data),
			}
		}
	}

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
		choice, err := json.Marshal(Choice{Rule: d.Choice.Rule, Excluded: d.Choice.Excluded})
		if err != nil {
			return err
		}
		contentSHA := fsx.HashBytes(d.Content)
		choiceSHA := fsx.HashBytes(choice)
		rel := fmt.Sprintf("roadmaps/%s/rev-%03d.md", slug, revNo)
		for {
			rel = fmt.Sprintf("roadmaps/%s/rev-%03d.md", slug, revNo)
			b, err := s.root.ReadFile(rel)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("reading %s: %w", rel, err) // e.g. ENOTDIR: retrying the next rev_no would loop forever
			}
			if errors.Is(err, fs.ErrNotExist) {
				if !s.root.Exists(ChoiceRel(rel)) {
					break
				}
			}
			if err == nil && fsx.HashBytes(b) == contentSHA {
				cb, cerr := s.root.ReadFile(ChoiceRel(rel))
				if errors.Is(cerr, fs.ErrNotExist) || (cerr == nil && fsx.HashBytes(cb) == choiceSHA) {
					break
				}
			}
			revNo++
		}
		res, err = tx.ExecContext(ctx,
			`INSERT INTO roadmap_revisions (roadmap_id, rev_no, rel_path, sha256, parsed_tree) VALUES (?, ?, ?, ?, ?)`,
			roadmapID, revNo, rel, contentSHA, string(tree))
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
		next, err := NextTopicSeq(ctx, tx, s.root)
		if err != nil {
			return err
		}
		seq := next - 1
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
				var slug, dir string
				for {
					seq++
					slug, dir = TopicDir(seq, t.Title)
					fi, err := s.root.Stat("topics/" + dir)
					if err != nil || !fi.IsDir() {
						break
					}
				}
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
				src := sources[i]
				w, err := s.recordSource(ctx, tx, o, t, src.data, src.sha)
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
		if _, err := s.writeOrAdopt(rel, d.Content, &undo); err != nil {
			return err
		}
		// The choice beside the revision lets hub rebuild re-select the same topics (CR-19).
		choiceRel := ChoiceRel(rel)
		if _, err := s.writeOrAdopt(choiceRel, choice, &undo); err != nil {
			return err
		}
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
func (s *Service) recordSource(ctx context.Context, tx *sql.Tx, o oldTopic, t roadmap.Topic, data []byte, sha string) (*sourceWrite, error) {
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
		ph := fsx.HashBytes(prev)
		for {
			histRel := path.Join("topics", o.Dir, "source-history", fmt.Sprintf("source.%04d.md", n))
			b, err := s.root.ReadFile(histRel)
			if errors.Is(err, fs.ErrNotExist) {
				w.hist = histRel
				break
			}
			if err == nil && fsx.HashBytes(b) == ph {
				w.hist = histRel
				break
			}
			n++
		}
		w.prev = prev
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
		if _, err := s.writeOrAdopt(w.rel, w.data, undo); err != nil {
			return err
		}
		return nil
	}
	histAdopted, err := s.writeOrAdopt(w.hist, w.prev, undo)
	if err != nil {
		return err
	}
	if err := s.root.WriteAtomic(w.rel, w.data, 0o644); err != nil {
		if !histAdopted {
			if rerr := s.root.Remove(w.hist); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
				slog.Warn("confirm writeSource: removing history failed", "hist", w.hist, "err", rerr)
			}
		}
		return fmt.Errorf("writing %s: %w", w.rel, err)
	}
	*undo = append(*undo, func() {
		if s.root.WriteAtomic(w.rel, w.prev, 0o644) == nil {
			if !histAdopted {
				if rerr := s.root.Remove(w.hist); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
					slog.Warn("confirm undo: removing history failed", "hist", w.hist, "err", rerr)
				}
			}
		}
	})
	return nil
}

// writeOrAdopt writes data to rel using WriteNew, unless rel already exists with identical
// SHA256 bytes, in which case it adopts the existing file without writing or adding an undo.
func (s *Service) writeOrAdopt(rel string, data []byte, undo *[]func()) (adopted bool, err error) {
	b, err := s.root.ReadFile(rel)
	if err == nil {
		if fsx.HashBytes(b) == fsx.HashBytes(data) {
			return true, nil // adopted identical file; not ours to delete on undo
		}
		return false, fmt.Errorf("%s exists with different content: %w", rel, fs.ErrExist)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("checking %s: %w", rel, err)
	}
	if err := s.root.WriteNew(rel, data, 0o644); err != nil {
		return false, fmt.Errorf("writing %s: %w", rel, err)
	}
	*undo = append(*undo, func() {
		if err := s.root.Remove(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("confirm undo: removing file failed", "rel", rel, "err", err)
		}
	})
	return false, nil
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

// NextTopicSeq returns 1 + the highest NN used by any topics.dir_name or any topics/NN-* directory on
// disk, so a new NN-slug is free in both (a rebuild or a leftover folder leaves gaps COUNT(*) misses).
// Call it inside the inserting transaction and increment it for each further topic.
func NextTopicSeq(ctx context.Context, tx *sql.Tx, root *fsx.Root) (int, error) {
	var seq int
	// CAST takes the leading integer of "NN-slug".
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(CAST(dir_name AS INTEGER)), 0) FROM topics`).Scan(&seq); err != nil {
		return 0, err
	}
	entries, err := fs.ReadDir(root.FS(), "topics")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue // a stray file fails WriteNew and rolls back; only folders reserve a number
		}
		num, _, ok := strings.Cut(e.Name(), "-")
		if n, err := strconv.Atoi(num); ok && err == nil {
			seq = max(seq, n)
		}
	}
	return seq + 1, nil
}

// TopicDir returns the slug and dir_name (NN-slug) for sequence number seq (see NextTopicSeq).
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
