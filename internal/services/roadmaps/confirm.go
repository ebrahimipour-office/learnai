package roadmaps

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"learninghub/internal/fsx"
	"learninghub/internal/roadmap"
)

var ErrNothingSelected = errors.New("no topics selected")

// maxDirSlugRunes keeps topics/<NN-slug>/... well under fsx's 220-byte path limit for Persian titles.
const maxDirSlugRunes = 40

// Confirm records the draft (T2.5, T2.7) in one transaction: the roadmap (new import) or its next
// revision (re-import), groups and topics, plus roadmaps/<slug>/rev-NNN.md written with WriteNew.
// Excluded topics are skipped, and so are groups left without topics. On re-import, topics are
// matched by stable key and updated in place (dir_name never changes); recorded topics and groups
// that are no longer kept are only archived, never deleted. The draft is consumed. On any failure
// no rows change and the revision file is not left behind.
func (s *Service) Confirm(ctx context.Context, id string) (roadmapID int64, topics int, err error) {
	d, err := s.Draft(ctx, id)
	if err != nil {
		return 0, 0, err
	}
	sel, err := d.Preview(d.Choice)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: %v", ErrInvalidSource, err)
	}
	kept := keptTopics(sel, d.Choice.Excluded)
	if len(kept) == 0 {
		return 0, 0, ErrNothingSelected
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
	var revPath string // set once the file exists, so a failed commit can remove it

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
			title := strings.TrimSuffix(d.SourceName, filepath.Ext(d.SourceName))
			if slug, err = s.freeRoadmapSlug(ctx, tx, title); err != nil {
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
			if _, err := tx.ExecContext(ctx, `UPDATE roadmaps SET select_rule = ? WHERE id = ?`, string(rule), roadmapID); err != nil {
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
		oldID := make(map[string]int64, len(old))
		for _, o := range old {
			oldID[o.Key] = o.ID
		}
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
			if tid, ok := oldID[t.StableKey]; ok {
				_, err := tx.ExecContext(ctx,
					`UPDATE topics SET group_id = ?, title = ?, title_norm = ?, outline = ?, ord = ?, state = 'active',
					   source_rev_id = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
					group, t.Title, roadmap.Normalize(t.Title), outline, float64(i+1), revID, tid)
				if err != nil {
					return fmt.Errorf("updating topic %q: %w", t.Title, err)
				}
				continue
			}
			seq++
			slug := slugOr(t.Title, "topic", maxDirSlugRunes)
			_, err := tx.ExecContext(ctx,
				`INSERT INTO topics (roadmap_id, group_id, stable_key, title, title_norm, outline, ord, slug, dir_name, source_rev_id)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				roadmapID, group, t.StableKey, t.Title, roadmap.Normalize(t.Title), outline,
				float64(i+1), slug, fmt.Sprintf("%02d-%s", seq, slug), revID)
			if err != nil {
				return fmt.Errorf("inserting topic %q: %w", t.Title, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE topics SET state = 'archived', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE roadmap_id = ? AND origin = 'roadmap' AND state = 'active' AND stable_key NOT IN (SELECT value FROM json_each(?))`,
			roadmapID, keysJSON(topicKeys)); err != nil {
			return fmt.Errorf("archiving topics: %w", err)
		}

		// The file goes last: any earlier failure leaves nothing on disk.
		if err := s.root.WriteNew(rel, d.Content, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", rel, err)
		}
		revPath = rel
		return nil
	})
	if err != nil {
		if revPath != "" { // commit failed after the write; we created it with O_EXCL, so it is ours
			_ = s.root.Remove(revPath)
		}
		return 0, 0, err
	}
	return roadmapID, len(kept), nil
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
