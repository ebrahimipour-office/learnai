// Package roadmaps holds roadmap import: drafts and preview (T2.4), confirm (T2.5), re-import diff (T2.7).
package roadmaps

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"learninghub/internal/fsx"
	"learninghub/internal/roadmap"
	"learninghub/internal/store"
)

const (
	MaxSourceBytes = 1 << 20 // R2.2: uploads up to 1 MB
	DraftTTL       = 24 * time.Hour
	tsFormat       = "2006-01-02T15:04:05.000Z" // same shape as SQLite strftime('%Y-%m-%dT%H:%M:%fZ')
)

var (
	ErrDraftNotFound = errors.New("draft not found or expired")
	ErrTooLarge      = errors.New("roadmap file is larger than 1 MB")
	ErrInvalidSource = errors.New("invalid roadmap file")
	ErrNoRoadmap     = errors.New("roadmap to re-import not found")
)

// Choice is the user's selection in the preview, stored as import_drafts.selection.
type Choice struct {
	Rule     roadmap.Rule `json:"rule"`
	Excluded []string     `json:"excluded,omitempty"` // stable keys of unticked topics
}

// Draft is an import preview. It never touches roadmaps, groups, topics or the file system.
type Draft struct {
	ID         string
	RoadmapID  int64 // 0 = new roadmap; otherwise a re-import of that roadmap (T2.7)
	SourceName string
	Content    []byte
	Choice     Choice
	ExpiresAt  time.Time
	AI         *Suggestion // T6.2: shown in the preview only; nil = never analyzed
	Title      string      // import_drafts.title; "" = new import: DefaultTitle, re-import: keep the current title
}

// MaxTitleRunes bounds a roadmap title edited in the preview.
const MaxTitleRunes = 200

// DefaultTitle is the first H1 of the file, or the file name without its extension (design §12.2).
func (d *Draft) DefaultTitle() string {
	if t := roadmap.Title(roadmap.Parse(d.Content)); t != "" {
		return t
	}
	return strings.TrimSuffix(d.SourceName, filepath.Ext(d.SourceName))
}

// Preview applies c to the draft content. Parsing is deterministic, so the content is the source of truth.
// A chapters or tree rule must pass roadmap.ValidateChapters (its *roadmap.ChapterError is returned as is).
func (d *Draft) Preview(c Choice) (*roadmap.Selection, error) {
	tree := roadmap.Parse(d.Content)
	if k := c.Rule.Kind; k == roadmap.RuleChapters || k == roadmap.RuleTree {
		if err := roadmap.ValidateChapters(tree, roadmap.LineCount(d.Content), c.Rule.Chapters, k == roadmap.RuleTree); err != nil {
			return nil, err
		}
	}
	return roadmap.Select(tree, c.Rule)
}

type Service struct {
	db   *store.DB
	root *fsx.Root
	Now  func() time.Time // replaceable in tests
}

func NewService(db *store.DB, root *fsx.Root) *Service {
	return &Service{db: db, root: root, Now: time.Now}
}

func (s *Service) ts(t time.Time) string { return t.UTC().Format(tsFormat) }

// CreateDraft validates an uploaded roadmap and stores it as a draft with the default rule.
func (s *Service) CreateDraft(ctx context.Context, name string, content []byte) (string, error) {
	return s.createDraft(ctx, 0, name, content)
}

// CreateReimport stores a draft for a new revision of an existing roadmap, starting from its saved rule.
func (s *Service) CreateReimport(ctx context.Context, roadmapID int64, name string, content []byte) (string, error) {
	return s.createDraft(ctx, roadmapID, name, content)
}

func (s *Service) createDraft(ctx context.Context, roadmapID int64, name string, content []byte) (string, error) {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case len(content) > MaxSourceBytes:
		return "", ErrTooLarge
	case ext != ".md" && ext != ".markdown":
		return "", fmt.Errorf("%w: only .md files are accepted", ErrInvalidSource)
	case len(name) > 255:
		return "", fmt.Errorf("%w: file name too long", ErrInvalidSource)
	case len(strings.TrimSpace(string(content))) == 0:
		return "", fmt.Errorf("%w: file is empty", ErrInvalidSource)
	case !utf8.Valid(content):
		return "", fmt.Errorf("%w: file is not valid UTF-8", ErrInvalidSource)
	}

	tree, err := json.Marshal(roadmap.Parse(content))
	if err != nil {
		return "", err
	}
	rule := roadmap.Rule{Kind: roadmap.RuleDeepest}
	var roadmapRef sql.NullInt64
	if roadmapID != 0 {
		var raw string
		err := s.db.Writer().QueryRowContext(ctx, `SELECT select_rule FROM roadmaps WHERE id = ?`, roadmapID).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNoRoadmap
		}
		if err != nil {
			return "", err
		}
		if err := json.Unmarshal([]byte(raw), &rule); err != nil {
			return "", fmt.Errorf("decoding roadmap rule: %w", err)
		}
		roadmapRef = sql.NullInt64{Int64: roadmapID, Valid: true}
		// Saved chapters point at lines of the old revision: keep them only if they still fit the new file.
		if k := rule.Kind; k == roadmap.RuleChapters || k == roadmap.RuleTree {
			if roadmap.ValidateChapters(roadmap.Parse(content), roadmap.LineCount(content), rule.Chapters, k == roadmap.RuleTree) != nil {
				rule = roadmap.Rule{Kind: roadmap.RuleDeepest}
				if k == roadmap.RuleTree {
					rule = roadmap.Rule{Kind: roadmap.RuleTree} // every heading: still one record per heading
				}
			}
		}
	}
	choice, err := json.Marshal(Choice{Rule: rule})
	if err != nil {
		return "", err
	}
	var idBytes [16]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(idBytes[:])

	if _, err := s.PurgeExpired(ctx); err != nil {
		return "", err
	}
	_, err = s.db.Writer().ExecContext(ctx,
		`INSERT INTO import_drafts (id, roadmap_id, source_name, content, content_sha, parsed_tree, selection, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, roadmapRef, name, string(content), fsx.HashBytes(content), string(tree), string(choice), s.ts(s.Now().Add(DraftTTL)))
	if err != nil {
		return "", fmt.Errorf("inserting draft: %w", err)
	}
	return id, nil
}

// Draft loads a draft that has not expired.
func (s *Service) Draft(ctx context.Context, id string) (*Draft, error) {
	var content, choice, expires string
	var roadmapID sql.NullInt64
	var suggestion, title sql.NullString
	d := &Draft{ID: id}
	err := s.db.Writer().QueryRowContext(ctx,
		`SELECT roadmap_id, source_name, content, selection, expires_at, ai_suggestion, title FROM import_drafts WHERE id = ? AND expires_at > ?`,
		id, s.ts(s.Now())).Scan(&roadmapID, &d.SourceName, &content, &choice, &expires, &suggestion, &title)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDraftNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(choice), &d.Choice); err != nil {
		return nil, fmt.Errorf("decoding draft selection: %w", err)
	}
	if suggestion.Valid {
		if err := json.Unmarshal([]byte(suggestion.String), &d.AI); err != nil {
			return nil, fmt.Errorf("decoding draft ai_suggestion: %w", err)
		}
	}
	d.Content = []byte(content)
	d.Title = title.String
	d.RoadmapID = roadmapID.Int64
	d.ExpiresAt, _ = time.Parse(tsFormat, expires)
	return d, nil
}

// SetChoice validates c against the draft and stores it.
func (s *Service) SetChoice(ctx context.Context, d *Draft, c Choice) error {
	if _, err := d.Preview(c); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSource, err)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	res, err := s.db.Writer().ExecContext(ctx,
		`UPDATE import_drafts SET selection = ? WHERE id = ? AND expires_at > ?`, string(raw), d.ID, s.ts(s.Now()))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrDraftNotFound
	}
	d.Choice = c
	return nil
}

// SetTitle stores the title chosen in the preview; "" clears it (see Draft.Title).
func (s *Service) SetTitle(ctx context.Context, d *Draft, title string) error {
	title = strings.TrimSpace(title)
	if utf8.RuneCountInString(title) > MaxTitleRunes {
		return fmt.Errorf("%w: title longer than %d characters", ErrInvalidSource, MaxTitleRunes)
	}
	res, err := s.db.Writer().ExecContext(ctx, `UPDATE import_drafts SET title = ? WHERE id = ? AND expires_at > ?`,
		sql.NullString{String: title, Valid: title != ""}, d.ID, s.ts(s.Now()))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrDraftNotFound
	}
	d.Title = title
	return nil
}

// PurgeExpired deletes drafts older than DraftTTL.
func (s *Service) PurgeExpired(ctx context.Context) (int64, error) {
	res, err := s.db.Writer().ExecContext(ctx, `DELETE FROM import_drafts WHERE expires_at <= ?`, s.ts(s.Now()))
	if err != nil {
		return 0, fmt.Errorf("purging expired drafts: %w", err)
	}
	return res.RowsAffected()
}

// RoadmapRef names a recorded roadmap for the re-import picker.
type RoadmapRef struct {
	ID    int64
	Title string
}

func (s *Service) Roadmaps(ctx context.Context) ([]RoadmapRef, error) {
	rows, err := s.db.Writer().QueryContext(ctx, `SELECT id, title FROM roadmaps ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RoadmapRef
	for rows.Next() {
		var r RoadmapRef
		if err := rows.Scan(&r.ID, &r.Title); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
