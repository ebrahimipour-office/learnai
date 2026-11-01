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

// Confirm records the draft as a new roadmap (T2.5): roadmaps, roadmap_revisions, groups and topics
// in one transaction, plus roadmaps/<slug>/rev-001.md written with WriteNew. Excluded topics are
// skipped, and so are groups left without topics. The draft is consumed. On any failure no rows
// remain and the revision file is not left behind.
func (s *Service) Confirm(ctx context.Context, id string) (roadmapID int64, topics int, err error) {
	d, err := s.Draft(ctx, id)
	if err != nil {
		return 0, 0, err
	}
	sel, err := d.Preview(d.Choice)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: %v", ErrInvalidSource, err)
	}
	excluded := map[string]bool{}
	for _, k := range d.Choice.Excluded {
		excluded[k] = true
	}
	var kept []roadmap.Topic
	usedGroup := make([]bool, len(sel.Groups))
	for _, t := range sel.Topics {
		if excluded[t.StableKey] {
			continue
		}
		kept = append(kept, t)
		for g := t.Group; g >= 0 && !usedGroup[g]; g = sel.Groups[g].Parent {
			usedGroup[g] = true
		}
	}
	if len(kept) == 0 {
		return 0, 0, ErrNothingSelected
	}

	tree, err := json.Marshal(roadmap.Parse(d.Content))
	if err != nil {
		return 0, 0, err
	}
	rule, err := json.Marshal(d.Choice.Rule)
	if err != nil {
		return 0, 0, err
	}
	title := strings.TrimSuffix(d.SourceName, filepath.Ext(d.SourceName))
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

		slug, err := s.freeRoadmapSlug(ctx, tx, title)
		if err != nil {
			return err
		}
		res, err = tx.ExecContext(ctx, `INSERT INTO roadmaps (slug, title, select_rule) VALUES (?, ?, ?)`, slug, title, string(rule))
		if err != nil {
			return fmt.Errorf("inserting roadmap: %w", err)
		}
		if roadmapID, err = res.LastInsertId(); err != nil {
			return err
		}
		rel := "roadmaps/" + slug + "/rev-001.md"
		res, err = tx.ExecContext(ctx,
			`INSERT INTO roadmap_revisions (roadmap_id, rev_no, rel_path, sha256, parsed_tree) VALUES (?, 1, ?, ?, ?)`,
			roadmapID, rel, fsx.HashBytes(d.Content), string(tree))
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
			res, err := tx.ExecContext(ctx,
				`INSERT INTO groups (roadmap_id, parent_id, stable_key, title, level, ord) VALUES (?, ?, ?, ?, ?, ?)`,
				roadmapID, parent, g.StableKey, g.Title, g.Level, float64(i+1))
			if err != nil {
				return fmt.Errorf("inserting group %q: %w", g.Title, err)
			}
			gid, err := res.LastInsertId()
			if err != nil {
				return err
			}
			groupID[i] = sql.NullInt64{Int64: gid, Valid: true}
		}

		// Topics are never deleted (only archived), so the count is a gap-free global sequence
		// that keeps dir_name unique across roadmaps.
		var existing int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM topics`).Scan(&existing); err != nil {
			return err
		}
		for i, t := range kept {
			var group sql.NullInt64
			if t.Group >= 0 {
				group = groupID[t.Group]
			}
			slug := slugOr(t.Title, "topic", maxDirSlugRunes)
			_, err := tx.ExecContext(ctx,
				`INSERT INTO topics (roadmap_id, group_id, stable_key, title, title_norm, outline, ord, slug, dir_name, source_rev_id)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				roadmapID, group, t.StableKey, t.Title, roadmap.Normalize(t.Title), sql.NullString{String: t.Outline, Valid: t.Outline != ""},
				float64(i+1), slug, fmt.Sprintf("%02d-%s", existing+i+1, slug), revID)
			if err != nil {
				return fmt.Errorf("inserting topic %q: %w", t.Title, err)
			}
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
